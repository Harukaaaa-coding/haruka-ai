package voiceconversation

import (
	"context"
	"errors"
	"strings"
	"sync"
)

var (
	ErrHubDraining = errors.New("voice service is draining")
	ErrHubCapacity = errors.New("voice connection capacity reached")
)

// Hub tracks live realtime connections independently from net/http.  A
// WebSocket has been hijacked after upgrade, so http.Server.Shutdown cannot be
// its sole lifecycle owner.  Each registration supplies a cancellation
// callback that causes the connection's active ASR/LLM/TTS turn to stop.
type Hub struct {
	mu         sync.Mutex
	draining   bool
	maxGlobal  int
	maxPerUser int
	byUser     map[string]int
	cancels    map[uint64]context.CancelFunc
	nextID     uint64
	empty      chan struct{}
}

func NewHub(maxGlobal, maxPerUser int) *Hub {
	if maxGlobal < 1 {
		maxGlobal = 32
	}
	if maxPerUser < 1 {
		maxPerUser = 1
	}
	empty := make(chan struct{})
	close(empty)
	return &Hub{
		maxGlobal:  maxGlobal,
		maxPerUser: maxPerUser,
		byUser:     make(map[string]int),
		cancels:    make(map[uint64]context.CancelFunc),
		empty:      empty,
	}
}

var defaultHub = NewHub(32, 1)

func DefaultHub() *Hub { return defaultHub }

func (h *Hub) Accepting() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.draining
}

// Register reserves one connection slot.  Call the returned release function
// exactly once (it is idempotent) when the WebSocket handler exits.
func (h *Hub) Register(userName string, cancel context.CancelFunc) (func(), error) {
	if h == nil {
		return nil, ErrHubDraining
	}
	userName = strings.TrimSpace(userName)
	if userName == "" || cancel == nil {
		return nil, ErrHubCapacity
	}

	h.mu.Lock()
	if h.draining {
		h.mu.Unlock()
		return nil, ErrHubDraining
	}
	if len(h.cancels) >= h.maxGlobal || h.byUser[userName] >= h.maxPerUser {
		h.mu.Unlock()
		return nil, ErrHubCapacity
	}
	if len(h.cancels) == 0 {
		h.empty = make(chan struct{})
	}
	h.nextID++
	id := h.nextID
	h.cancels[id] = cancel
	h.byUser[userName]++
	h.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.cancels, id)
			h.byUser[userName]--
			if h.byUser[userName] <= 0 {
				delete(h.byUser, userName)
			}
			if len(h.cancels) == 0 {
				close(h.empty)
			}
			h.mu.Unlock()
		})
	}, nil
}

// BeginDrain rejects new connections and cancels every active turn.  The
// callbacks run outside the hub lock because a cancellation can synchronously
// wake a WebSocket writer.
func (h *Hub) BeginDrain() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.draining {
		h.mu.Unlock()
		return
	}
	h.draining = true
	cancels := make([]context.CancelFunc, 0, len(h.cancels))
	for _, cancel := range h.cancels {
		cancels = append(cancels, cancel)
	}
	h.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (h *Hub) Wait(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h.mu.Lock()
	empty := h.empty
	h.mu.Unlock()
	select {
	case <-empty:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
