package model

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Message struct {
	ID uint `gorm:"primaryKey;autoIncrement" json:"id"`
	// MessageID is generated before a message leaves the process and is the
	// durable idempotency key for the asynchronous RabbitMQ -> MySQL path.
	// It intentionally differs from the database auto-increment ID: a publish
	// confirmation can be lost after RabbitMQ accepted a message, so the same
	// logical message may be delivered more than once.
	MessageID string    `gorm:"type:varchar(36);not null;uniqueIndex:idx_messages_message_id" json:"message_id"`
	SessionID string    `gorm:"index;index:idx_message_owner_session_created,priority:2;not null;type:varchar(36)" json:"session_id"`
	UserName  string    `gorm:"type:varchar(50);not null;index:idx_message_owner_session_created,priority:1" json:"username"`
	Content   string    `gorm:"type:text" json:"content"`
	IsUser    bool      `gorm:"not null;" json:"is_user"`
	Citations string    `gorm:"type:longtext" json:"-"`
	CreatedAt time.Time `gorm:"index:idx_message_owner_session_created,priority:3" json:"created_at"`
}

// NewMessageID returns an opaque, globally unique event ID suitable for the
// messages.message_id unique index and AMQP MessageId property.
func NewMessageID() string {
	return uuid.NewString()
}

// EnsureMessageID makes a message safe to retry. Only UUIDs are accepted so
// the value always fits the schema and has one canonical representation.
func (message *Message) EnsureMessageID() string {
	if message == nil {
		return ""
	}
	if parsed, err := uuid.Parse(strings.TrimSpace(message.MessageID)); err == nil {
		message.MessageID = parsed.String()
		return message.MessageID
	}
	message.MessageID = NewMessageID()
	return message.MessageID
}

// BeforeCreate protects direct GORM callers as well as DAO callers. The DAO
// still calls EnsureMessageID before enqueueing so all retries carry the same
// key, rather than generating a fresh one during consumption.
func (message *Message) BeforeCreate(_ *gorm.DB) error {
	message.EnsureMessageID()
	return nil
}

type History struct {
	MessageID string               `json:"message_id,omitempty"`
	IsUser    bool                 `json:"is_user"`
	Content   string               `json:"content"`
	Citations []KnowledgeReference `json:"citations,omitempty"`
}

func (message *Message) SetKnowledgeReferences(references []KnowledgeReference) error {
	if message == nil || len(references) == 0 {
		if message != nil {
			message.Citations = ""
		}
		return nil
	}
	encoded, err := json.Marshal(references)
	if err != nil {
		return err
	}
	message.Citations = string(encoded)
	return nil
}

func (message *Message) KnowledgeReferences() []KnowledgeReference {
	if message == nil || message.Citations == "" {
		return nil
	}
	var references []KnowledgeReference
	if err := json.Unmarshal([]byte(message.Citations), &references); err != nil {
		return nil
	}
	return references
}
