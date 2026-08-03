package rabbitmq

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

var (
	rmqMessageMu  sync.RWMutex
	RMQMessage    *RabbitMQ
	rmqCancel     context.CancelFunc
	rmqConsumerWG sync.WaitGroup
)

func currentMessageQueue() *RabbitMQ {
	rmqMessageMu.RLock()
	defer rmqMessageMu.RUnlock()
	return RMQMessage
}

func InitRabbitMQ(contexts ...context.Context) error {
	rmqMessageMu.Lock()
	defer rmqMessageMu.Unlock()

	if RMQMessage != nil {
		return nil
	}
	queue, err := NewWorkRabbitMQ("Message")
	if err != nil {
		return err
	}
	RMQMessage = queue
	parent := context.Background()
	if len(contexts) > 0 && contexts[0] != nil {
		parent = contexts[0]
	}
	consumerCtx, cancel := context.WithCancel(parent)
	rmqCancel = cancel
	rmqConsumerWG.Add(1)
	go func() {
		defer rmqConsumerWG.Done()
		consumeWithReconnect(consumerCtx, queue)
		disableMessageQueue(queue)
	}()
	return nil
}

// consumeWithReconnect keeps the durable worker alive across broker, network
// and channel failures. A consumer's delivery channel closing is not a clean
// shutdown: pending messages remain in RabbitMQ and can safely be redelivered
// because MessageID makes the MySQL write idempotent.
func consumeWithReconnect(ctx context.Context, queue *RabbitMQ) {
	delay := reconnectInitialDelay
	for {
		err := queue.Consume(ctx, MQMessage)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("RabbitMQ consumer interrupted: %v; reconnecting", err)
		}

		if err := waitReconnect(ctx, delay); err != nil {
			return
		}
		if err := queue.Reconnect(); err != nil {
			log.Printf("RabbitMQ consumer reconnect failed: %v", err)
			delay = min(delay*2, reconnectMaximumDelay)
			continue
		}
		delay = reconnectInitialDelay
	}
}

func waitReconnect(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func disableMessageQueue(queue *RabbitMQ) {
	rmqMessageMu.Lock()
	var cancel context.CancelFunc
	if RMQMessage == queue {
		RMQMessage = nil
		cancel = rmqCancel
		rmqCancel = nil
	}
	rmqMessageMu.Unlock()
	if cancel != nil {
		cancel()
	}
	queue.Destroy()
}

func DestroyRabbitMQ() {
	_ = ShutdownRabbitMQ(context.Background())
}

func ShutdownRabbitMQ(ctx context.Context) error {
	rmqMessageMu.Lock()
	queue := RMQMessage
	cancel := rmqCancel
	RMQMessage = nil
	rmqCancel = nil
	rmqMessageMu.Unlock()

	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		rmqConsumerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		if queue != nil {
			queue.Destroy()
		}
		return nil
	case <-ctx.Done():
		if queue != nil {
			queue.Destroy()
		}
		return errors.Join(ctx.Err(), errors.New("RabbitMQ consumer shutdown timed out"))
	}
}

func Available() bool {
	queue := currentMessageQueue()
	return queue != nil && queue.Available()
}
