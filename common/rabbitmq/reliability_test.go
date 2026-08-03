package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"GopherAI/model"

	"github.com/google/uuid"
	"github.com/streadway/amqp"
)

func TestGenerateMessageMQParamIncludesEventID(t *testing.T) {
	data := GenerateMessageMQParam("session-1", "hello", "alice", true)
	var param MessageMQParam
	if err := json.Unmarshal(data, &param); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if param.MessageID == "" {
		t.Fatal("payload omitted message_id")
	}
	if _, err := uuid.Parse(param.MessageID); err != nil {
		t.Fatalf("message_id is not a UUID: %v", err)
	}
	if param.SessionID != "session-1" || param.Content != "hello" || param.UserName != "alice" || !param.IsUser {
		t.Fatalf("unexpected payload: %#v", param)
	}
}

func TestMQMessageKeepsTheSameEventIDAcrossRedeliveries(t *testing.T) {
	messageID := uuid.NewString()
	body, err := json.Marshal(MessageMQParam{
		MessageID: messageID,
		SessionID: "session-1",
		Content:   "hello",
		UserName:  "alice",
		IsUser:    true,
	})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	var saved []model.Message
	persist := func(message *model.Message) (*model.Message, error) {
		saved = append(saved, *message)
		return message, nil
	}

	for delivery := 0; delivery < 2; delivery++ {
		if err := persistDelivery(&amqp.Delivery{Body: body, MessageId: uuid.NewString()}, persist); err != nil {
			t.Fatalf("consume delivery %d: %v", delivery, err)
		}
	}
	if len(saved) != 2 {
		t.Fatalf("saved deliveries = %d, want 2", len(saved))
	}
	for index, savedMessage := range saved {
		if savedMessage.MessageID != messageID {
			t.Fatalf("delivery %d message ID = %q, want %q", index, savedMessage.MessageID, messageID)
		}
	}
}

func TestMQMessageUsesDeterministicIDForLegacyPayload(t *testing.T) {
	body := []byte(`{"session_id":"session-1","content":"legacy","user_name":"alice","is_user":true}`)
	var saved []string
	persist := func(message *model.Message) (*model.Message, error) {
		saved = append(saved, message.MessageID)
		return message, nil
	}

	for delivery := 0; delivery < 2; delivery++ {
		if err := persistDelivery(&amqp.Delivery{Body: body}, persist); err != nil {
			t.Fatalf("consume legacy delivery %d: %v", delivery, err)
		}
	}
	if len(saved) != 2 || saved[0] == "" || saved[0] != saved[1] {
		t.Fatalf("legacy payload did not get a stable ID: %#v", saved)
	}
}

func TestMQMessageRejectsInvalidJSONBeforePersistence(t *testing.T) {
	called := false
	persist := func(message *model.Message) (*model.Message, error) {
		called = true
		return message, nil
	}

	if err := persistDelivery(&amqp.Delivery{Body: []byte("not-json")}, persist); err == nil {
		t.Fatal("invalid JSON should fail so the bounded retry/DLQ path handles it")
	}
	if called {
		t.Fatal("invalid JSON must not reach persistence")
	}
}

func TestPersistMessageFallbackKeepsItsEventID(t *testing.T) {
	var persisted *model.Message
	persist := func(message *model.Message) (*model.Message, error) {
		persisted = message
		return message, nil
	}
	input := &model.Message{SessionID: "session-1", UserName: "alice", Content: "hello", IsUser: true}
	result, err := persistMessage(input, nil, persist)
	if err != nil {
		t.Fatalf("persist fallback: %v", err)
	}
	if result != input || persisted != input {
		t.Fatal("fallback did not return the original persisted message")
	}
	if input.MessageID == "" {
		t.Fatal("fallback persisted a message without an event ID")
	}
	eventID := input.MessageID
	if again := input.EnsureMessageID(); again != eventID {
		t.Fatalf("fallback event ID changed after retry: %q vs %q", eventID, again)
	}
}

func TestRetryHeaderAndQueueHelpers(t *testing.T) {
	tests := []struct {
		name    string
		headers amqp.Table
		want    int
	}{
		{name: "missing", want: 0},
		{name: "int32", headers: amqp.Table{messageRetryHeader: int32(2)}, want: 2},
		{name: "string", headers: amqp.Table{messageRetryHeader: "3"}, want: 3},
		{name: "negative", headers: amqp.Table{messageRetryHeader: int(-1)}, want: 0},
		{name: "bounded", headers: amqp.Table{messageRetryHeader: uint64(99)}, want: maxMessageRetries},
		{name: "invalid", headers: amqp.Table{messageRetryHeader: "oops"}, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := deliveryRetryCount(test.headers); got != test.want {
				t.Fatalf("retry count = %d, want %d", got, test.want)
			}
		})
	}
	if got := deadLetterQueueName("Message"); got != "Message.dlq" {
		t.Fatalf("dead letter queue = %q", got)
	}
}

func TestCloneHeadersAndBackoff(t *testing.T) {
	headers := amqp.Table{"key": "value"}
	clone := cloneHeaders(headers)
	clone["key"] = "changed"
	if headers["key"] != "value" {
		t.Fatal("cloning headers mutated the source delivery")
	}

	if got := reconnectDelay(0); got != reconnectInitialDelay {
		t.Fatalf("initial reconnect delay = %s, want %s", got, reconnectInitialDelay)
	}
	if got := reconnectDelay(99); got != reconnectMaximumDelay {
		t.Fatalf("bounded reconnect delay = %s, want %s", got, reconnectMaximumDelay)
	}
}

func TestWaitReconnectHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err := waitReconnect(ctx, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait reconnect error = %v, want context cancellation", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("cancelled reconnect wait took %s", elapsed)
	}
}
