package rabbitmq

import (
	"GopherAI/config"
	"GopherAI/model"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/streadway/amqp"
)

const (
	messageRetryHeader      = "x-gopherai-retry-count"
	messageLastErrorHeader  = "x-gopherai-last-error"
	messageDeadLetterHeader = "x-gopherai-dead-lettered-at"
	messageOriginalQueue    = "x-gopherai-original-queue"

	maxMessageRetries      = 3
	maxReconnectAttempts   = 3
	publishConfirmTimeout  = 5 * time.Second
	reconnectInitialDelay  = 200 * time.Millisecond
	reconnectMaximumDelay  = 2 * time.Second
	maxFailureHeaderLength = 512
)

var (
	ErrPublisherUnavailable = errors.New("RabbitMQ publisher is not initialized")
	ErrConsumerUnavailable  = errors.New("RabbitMQ consumer is not initialized")
	ErrConsumerStopped      = errors.New("RabbitMQ consumer delivery channel closed")
	ErrPublishUnconfirmed   = errors.New("RabbitMQ publish was not confirmed")
)

// RabbitMQ owns one work-queue connection. The mutex serializes publishing so
// publisher confirms can be matched safely without a second dependency. A
// failed publish invalidates the channel and the next operation reconnects.
type RabbitMQ struct {
	conn          *amqp.Connection
	channel       *amqp.Channel
	confirmations <-chan amqp.Confirmation
	Exchange      string
	Key           string

	mu          sync.Mutex
	dial        func() (*amqp.Connection, error)
	destroyed   bool
	consumerSeq atomic.Uint64
}

func NewRabbitMQ(exchange string, key string) *RabbitMQ {
	return &RabbitMQ{
		Exchange: exchange,
		Key:      key,
		dial:     newConnection,
	}
}

func newConnection() (*amqp.Connection, error) {
	c := config.GetConfig()
	mqURL := &url.URL{
		Scheme: "amqp",
		User:   url.UserPassword(c.RabbitmqUsername, c.RabbitmqPassword),
		Host:   fmt.Sprintf("%s:%d", c.RabbitmqHost, c.RabbitmqPort),
		Path:   c.RabbitmqVhost,
	}
	connection, err := amqp.Dial(mqURL.String())
	if err != nil {
		return nil, fmt.Errorf("connect to RabbitMQ at %s: %w", mqURL.Host, err)
	}
	return connection, nil
}

func NewWorkRabbitMQ(queue string) (*RabbitMQ, error) {
	rabbitmq := NewRabbitMQ("", queue)
	rabbitmq.mu.Lock()
	err := rabbitmq.connectLocked()
	rabbitmq.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return rabbitmq, nil
}

func (r *RabbitMQ) Destroy() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.destroyed = true
	r.closeLocked()
	r.mu.Unlock()
}

// Publish sends a durable message and waits for RabbitMQ's publisher
// confirmation. A confirmation timeout is deliberately treated as a failure:
// PersistMessage then writes the same MessageID to MySQL, and the unique index
// makes a later broker delivery harmless.
func (r *RabbitMQ) Publish(message []byte) error {
	return r.publish(message, nil, model.NewMessageID(), r.Key, r.Exchange)
}

func (r *RabbitMQ) publish(message []byte, headers amqp.Table, messageID, routingKey, exchange string) error {
	if r == nil {
		return ErrPublisherUnavailable
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		r.mu.Lock()
		err := r.publishLocked(message, headers, messageID, routingKey, exchange)
		if err != nil {
			r.closeLocked()
		}
		r.mu.Unlock()
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("publish to %q: %w", routingKey, lastErr)
}

func (r *RabbitMQ) publishLocked(message []byte, headers amqp.Table, messageID, routingKey, exchange string) error {
	if r.destroyed {
		return ErrPublisherUnavailable
	}
	if err := r.ensureConnectedLocked(); err != nil {
		return err
	}
	if _, err := r.declareQueuesLocked(); err != nil {
		return fmt.Errorf("declare message queues: %w", err)
	}
	if r.confirmations == nil {
		return ErrPublishUnconfirmed
	}
	publishing := amqp.Publishing{
		Headers:         cloneHeaders(headers),
		ContentType:     "application/json",
		ContentEncoding: "utf-8",
		DeliveryMode:    amqp.Persistent,
		MessageId:       messageID,
		Timestamp:       time.Now().UTC(),
		Body:            message,
	}
	if err := r.channel.Publish(exchange, routingKey, false, false, publishing); err != nil {
		return err
	}
	select {
	case confirmation, ok := <-r.confirmations:
		if !ok || !confirmation.Ack {
			return ErrPublishUnconfirmed
		}
		return nil
	case <-time.After(publishConfirmTimeout):
		return fmt.Errorf("%w after %s", ErrPublishUnconfirmed, publishConfirmTimeout)
	}
}

// Consume does not let a malformed or failing message terminate the worker.
// Failed handlers are republished with a bounded retry header, then copied to
// a durable sibling DLQ. Transport failures still return to the caller so the
// Init loop can reconnect.
func (r *RabbitMQ) Consume(ctx context.Context, handle func(msg *amqp.Delivery) error) error {
	if r == nil {
		return ErrConsumerUnavailable
	}
	if handle == nil {
		return errors.New("RabbitMQ consumer handler is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	if r.destroyed {
		r.mu.Unlock()
		return ErrConsumerUnavailable
	}
	if err := r.ensureConnectedLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	q, err := r.declareQueuesLocked()
	if err != nil {
		r.closeLocked()
		r.mu.Unlock()
		return fmt.Errorf("declare message queues: %w", err)
	}
	if err := r.channel.Qos(1, 0, false); err != nil {
		r.closeLocked()
		r.mu.Unlock()
		return fmt.Errorf("set consumer qos: %w", err)
	}
	channel := r.channel
	consumerTag := r.nextConsumerTag()
	msgs, err := channel.Consume(q.Name, consumerTag, false, false, false, false, nil)
	r.mu.Unlock()
	if err != nil {
		return fmt.Errorf("start message consumer: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			// Channel cancellation can race with a broker close; either result is
			// safe because the shutdown context owns the worker lifecycle.
			_ = channel.Cancel(consumerTag, false)
			return nil
		case msg, ok := <-msgs:
			if !ok {
				return ErrConsumerStopped
			}
			if err := r.processDelivery(&msg, handle); err != nil {
				return err
			}
		}
	}
}

func (r *RabbitMQ) processDelivery(msg *amqp.Delivery, handle func(msg *amqp.Delivery) error) error {
	if err := handle(msg); err == nil {
		if err := msg.Ack(false); err != nil {
			return fmt.Errorf("ack message: %w", err)
		}
		return nil
	} else {
		return r.retryOrDeadLetter(msg, err)
	}
}

func (r *RabbitMQ) retryOrDeadLetter(msg *amqp.Delivery, cause error) error {
	headers := cloneHeaders(msg.Headers)
	retryCount := deliveryRetryCount(headers)
	headers[messageLastErrorHeader] = truncateHeaderValue(cause.Error())
	messageID := messageIDFromDelivery(msg)

	routingKey := r.Key
	exchange := r.Exchange
	if retryCount >= maxMessageRetries {
		headers[messageDeadLetterHeader] = time.Now().UTC().Format(time.RFC3339Nano)
		headers[messageOriginalQueue] = r.Key
		routingKey = deadLetterQueueName(r.Key)
		exchange = ""
	} else {
		headers[messageRetryHeader] = int32(retryCount + 1)
	}

	// Republish before acknowledging the original. If this fails the original
	// is requeued, preserving at-least-once delivery rather than losing data.
	if err := r.publish(msg.Body, headers, messageID, routingKey, exchange); err != nil {
		if nackErr := msg.Nack(false, true); nackErr != nil {
			return fmt.Errorf("republish failed: %v; requeue failed: %w", err, nackErr)
		}
		return fmt.Errorf("republish failed; message requeued: %w", err)
	}
	if err := msg.Ack(false); err != nil {
		return fmt.Errorf("ack after republish: %w", err)
	}
	return nil
}

func (r *RabbitMQ) ensureConnectedLocked() error {
	if r.destroyed {
		return ErrPublisherUnavailable
	}
	if r.conn != nil && !r.conn.IsClosed() && r.channel != nil {
		return nil
	}
	r.closeLocked()

	var lastErr error
	for attempt := 0; attempt < maxReconnectAttempts; attempt++ {
		if err := r.connectLocked(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt+1 < maxReconnectAttempts {
			time.Sleep(reconnectDelay(attempt))
		}
	}
	return fmt.Errorf("reconnect RabbitMQ after %d attempts: %w", maxReconnectAttempts, lastErr)
}

func (r *RabbitMQ) connectLocked() error {
	if r.dial == nil {
		r.dial = newConnection
	}
	connection, err := r.dial()
	if err != nil {
		return err
	}
	channel, err := connection.Channel()
	if err != nil {
		_ = connection.Close()
		return fmt.Errorf("open RabbitMQ channel: %w", err)
	}
	if err := channel.Confirm(false); err != nil {
		_ = channel.Close()
		_ = connection.Close()
		return fmt.Errorf("enable RabbitMQ publisher confirms: %w", err)
	}

	r.conn = connection
	r.channel = channel
	r.confirmations = channel.NotifyPublish(make(chan amqp.Confirmation, 1))
	return nil
}

func (r *RabbitMQ) closeLocked() {
	if r.channel != nil {
		_ = r.channel.Close()
		r.channel = nil
	}
	if r.conn != nil {
		_ = r.conn.Close()
		r.conn = nil
	}
	r.confirmations = nil
}

func (r *RabbitMQ) declareQueuesLocked() (amqp.Queue, error) {
	q, err := r.channel.QueueDeclare(r.Key, true, false, false, false, nil)
	if err != nil {
		return amqp.Queue{}, err
	}
	if _, err := r.channel.QueueDeclare(deadLetterQueueName(r.Key), true, false, false, false, nil); err != nil {
		return amqp.Queue{}, err
	}
	return q, nil
}

func (r *RabbitMQ) nextConsumerTag() string {
	sequence := r.consumerSeq.Add(1)
	return "gopherai-message-consumer-" + strconv.FormatUint(sequence, 10)
}

func (r *RabbitMQ) Available() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.destroyed && r.conn != nil && !r.conn.IsClosed() && r.channel != nil
}

// Reconnect forcefully replaces a possibly half-closed channel. Consume uses
// this after its delivery stream closes; Publish already performs the same
// recovery internally after a failed confirmation.
func (r *RabbitMQ) Reconnect() error {
	if r == nil {
		return ErrPublisherUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.destroyed {
		return ErrPublisherUnavailable
	}
	r.closeLocked()
	return r.ensureConnectedLocked()
}

func deadLetterQueueName(queue string) string {
	return queue + ".dlq"
}

func reconnectDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return reconnectInitialDelay
	}
	delay := reconnectInitialDelay
	for step := 0; step < attempt && delay < reconnectMaximumDelay; step++ {
		delay *= 2
	}
	if delay > reconnectMaximumDelay {
		return reconnectMaximumDelay
	}
	return delay
}

func cloneHeaders(headers amqp.Table) amqp.Table {
	if len(headers) == 0 {
		return amqp.Table{}
	}
	copy := make(amqp.Table, len(headers))
	for key, value := range headers {
		copy[key] = value
	}
	return copy
}

func deliveryRetryCount(headers amqp.Table) int {
	if headers == nil {
		return 0
	}
	value, exists := headers[messageRetryHeader]
	if !exists {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return max(0, typed)
	case int8:
		return max(0, int(typed))
	case int16:
		return max(0, int(typed))
	case int32:
		return max(0, int(typed))
	case int64:
		if typed > int64(maxMessageRetries) {
			return maxMessageRetries
		}
		return max(0, int(typed))
	case uint:
		if typed > uint(maxMessageRetries) {
			return maxMessageRetries
		}
		return int(typed)
	case uint8:
		return int(typed)
	case uint16:
		return int(typed)
	case uint32:
		if typed > uint32(maxMessageRetries) {
			return maxMessageRetries
		}
		return int(typed)
	case uint64:
		if typed > uint64(maxMessageRetries) {
			return maxMessageRetries
		}
		return int(typed)
	case string:
		parsed, err := strconv.Atoi(typed)
		if err != nil {
			return 0
		}
		return max(0, min(parsed, maxMessageRetries))
	default:
		return 0
	}
}

func truncateHeaderValue(value string) string {
	runes := []rune(value)
	if len(runes) <= maxFailureHeaderLength {
		return value
	}
	return string(runes[:maxFailureHeaderLength])
}
