package rabbitmq

import (
	"GopherAI/dao/message"
	"GopherAI/model"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/streadway/amqp"
)

// MessageMQParam is deliberately self-contained. MessageID is both the AMQP
// MessageId property and the MySQL idempotency key, so a publish retry and a
// consumer redelivery describe the same logical chat message.
type MessageMQParam struct {
	MessageID string `json:"message_id"`
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
	UserName  string `json:"user_name"`
	IsUser    bool   `json:"is_user"`
	Citations string `json:"citations,omitempty"`
}

type messagePersister func(*model.Message) (*model.Message, error)

// GenerateMessageMQParam is retained for compatibility with older callers.
// New persistence paths use PersistMessage so the generated ID also lives on
// the in-memory Message before the first publish attempt.
func GenerateMessageMQParam(sessionID string, content string, userName string, isUser bool) []byte {
	data, err := json.Marshal(MessageMQParam{
		MessageID: model.NewMessageID(),
		SessionID: sessionID,
		Content:   content,
		UserName:  userName,
		IsUser:    isUser,
	})
	if err != nil {
		return nil
	}
	return data
}

func messageMQParam(msg *model.Message) MessageMQParam {
	msg.EnsureMessageID()
	return MessageMQParam{
		MessageID: msg.MessageID,
		SessionID: msg.SessionID,
		Content:   msg.Content,
		UserName:  msg.UserName,
		IsUser:    msg.IsUser,
		Citations: msg.Citations,
	}
}

func encodeMessageMQParam(msg *model.Message) ([]byte, error) {
	if msg == nil {
		return nil, fmt.Errorf("message is nil")
	}
	return json.Marshal(messageMQParam(msg))
}

// MQMessage persists a delivery exactly once. CreateMessage returns the
// already-created row for a duplicate MessageID, which lets us Ack safely even
// after RabbitMQ redelivered a record whose first Ack was lost.
func MQMessage(msg *amqp.Delivery) error {
	return persistDelivery(msg, message.CreateMessage)
}

func persistDelivery(msg *amqp.Delivery, persist messagePersister) error {
	if msg == nil {
		return fmt.Errorf("RabbitMQ delivery is nil")
	}
	if persist == nil {
		return fmt.Errorf("message persister is nil")
	}
	var param MessageMQParam
	if err := json.Unmarshal(msg.Body, &param); err != nil {
		return fmt.Errorf("decode message payload: %w", err)
	}
	newMsg := &model.Message{
		MessageID: messageIDForPayload(param.MessageID, msg.MessageId, msg.Body),
		SessionID: param.SessionID,
		Content:   param.Content,
		UserName:  param.UserName,
		IsUser:    param.IsUser,
		Citations: param.Citations,
	}
	_, err := persist(newMsg)
	return err
}

// PersistMessage prefers the asynchronous queue and falls back to MySQL when
// RabbitMQ has not been initialized or publishing fails. The same MessageID
// is used for both attempts, making an uncertain broker confirmation safe.
func PersistMessage(msg *model.Message) (*model.Message, error) {
	return persistMessage(msg, currentMessageQueue(), message.CreateMessage)
}

func persistMessage(msg *model.Message, queue *RabbitMQ, persist messagePersister) (*model.Message, error) {
	if msg == nil {
		return nil, fmt.Errorf("persist message: message is nil")
	}
	if persist == nil {
		return nil, fmt.Errorf("persist message: persister is nil")
	}
	msg.EnsureMessageID()
	if queue == nil {
		persisted, err := persist(msg)
		if err != nil {
			return nil, fmt.Errorf("persist message without RabbitMQ: %w", err)
		}
		return persisted, nil
	}

	data, err := encodeMessageMQParam(msg)
	if err == nil {
		err = queue.publish(data, nil, msg.MessageID, queue.Key, queue.Exchange)
	}
	if err == nil {
		return msg, nil
	}

	persisted, dbErr := persist(msg)
	if dbErr != nil {
		return nil, fmt.Errorf("publish message: %v; MySQL fallback: %w", err, dbErr)
	}
	return persisted, nil
}

// messageIDForPayload understands current UUID event IDs and gives legacy
// queue records a deterministic UUIDv5. The latter avoids generating a fresh
// ID every time an old pre-idempotency message is redelivered.
func messageIDForPayload(payloadID, transportID string, body []byte) string {
	if id, ok := canonicalMessageID(payloadID); ok {
		return id
	}
	if id, ok := canonicalMessageID(transportID); ok {
		return id
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, append([]byte("gopherai:legacy-message:"), body...)).String()
}

func canonicalMessageID(value string) (string, bool) {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	return parsed.String(), true
}

func messageIDFromDelivery(msg *amqp.Delivery) string {
	if msg == nil {
		return model.NewMessageID()
	}
	var param MessageMQParam
	if err := json.Unmarshal(msg.Body, &param); err == nil {
		return messageIDForPayload(param.MessageID, msg.MessageId, msg.Body)
	}
	return messageIDForPayload("", msg.MessageId, msg.Body)
}
