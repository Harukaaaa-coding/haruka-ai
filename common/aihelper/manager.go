package aihelper

import (
	"context"
	"sync"
	"time"
)

var ctx = context.Background()

// defaultHelperIdleTTL bounds how long a hydrated session helper can remain
// resident after its last use. The full chat history is durable, so evicting
// an idle helper only releases its model and in-memory context; a later
// request rehydrates the recent history through the session service.
const defaultHelperIdleTTL = 30 * time.Minute

// AIHelperManager manages the mapping from user/session pairs to AI helpers.
// Access is synchronized because the manager is shared by concurrent HTTP
// requests.
type AIHelperManager struct {
	helpers    map[string]map[string]*AIHelper
	lastAccess map[string]map[string]time.Time
	idleTTL    time.Duration
	clock      func() time.Time
	mu         sync.RWMutex
}

// NewAIHelperManager creates a new manager instance.
func NewAIHelperManager() *AIHelperManager {
	return &AIHelperManager{
		helpers:    make(map[string]map[string]*AIHelper),
		lastAccess: make(map[string]map[string]time.Time),
		idleTTL:    defaultHelperIdleTTL,
		clock:      time.Now,
	}
}

// GetOrCreateAIHelper returns a session helper, creating it when needed. It
// also performs a lazy eviction pass so inactive sessions do not remain in
// process memory indefinitely.
func (m *AIHelperManager) GetOrCreateAIHelper(userName string, sessionID string, modelType string, config map[string]interface{}) (*AIHelper, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	m.evictIdleHelpersLocked(now)

	userHelpers, exists := m.helpers[userName]
	if !exists {
		userHelpers = make(map[string]*AIHelper)
		m.helpers[userName] = userHelpers
	}

	// Switching a session model preserves the existing in-memory history.
	helper, exists := userHelpers[sessionID]
	if exists {
		m.touchLocked(userName, sessionID, now)
		if helper.GetModelType() == modelType {
			return helper, nil
		}

		factory := GetGlobalFactory()
		aiModel, err := factory.CreateAIModel(ctx, modelType, config)
		if err != nil {
			return nil, err
		}
		if err := helper.SetModel(aiModel); err != nil {
			return nil, err
		}
		return helper, nil
	}

	factory := GetGlobalFactory()
	helper, err := factory.CreateAIHelper(ctx, modelType, sessionID, config)
	if err != nil {
		return nil, err
	}

	userHelpers[sessionID] = helper
	m.touchLocked(userName, sessionID, now)
	return helper, nil
}

// GetAIHelper gets a helper that is already resident. Reading a helper counts
// as use, so an active caller cannot race a later lazy eviction pass.
func (m *AIHelperManager) GetAIHelper(userName string, sessionID string) (*AIHelper, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	userHelpers, exists := m.helpers[userName]
	if !exists {
		return nil, false
	}

	helper, exists := userHelpers[sessionID]
	if exists {
		m.touchLocked(userName, sessionID, m.now())
	}
	return helper, exists
}

// RemoveAIHelper removes a user's session helper explicitly.
func (m *AIHelperManager) RemoveAIHelper(userName string, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	userHelpers, exists := m.helpers[userName]
	if !exists {
		return
	}

	delete(userHelpers, sessionID)
	if userLastAccess, ok := m.lastAccess[userName]; ok {
		delete(userLastAccess, sessionID)
		if len(userLastAccess) == 0 {
			delete(m.lastAccess, userName)
		}
	}

	if len(userHelpers) == 0 {
		delete(m.helpers, userName)
	}
}

// GetUserSessions returns the session IDs whose helpers are currently
// resident. Durable session listing belongs to the session DAO.
func (m *AIHelperManager) GetUserSessions(userName string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	userHelpers, exists := m.helpers[userName]
	if !exists {
		return []string{}
	}

	sessionIDs := make([]string, 0, len(userHelpers))
	for sessionID := range userHelpers {
		sessionIDs = append(sessionIDs, sessionID)
	}

	return sessionIDs
}

func (m *AIHelperManager) now() time.Time {
	if m.clock == nil {
		return time.Now()
	}
	return m.clock()
}

func (m *AIHelperManager) touchLocked(userName string, sessionID string, now time.Time) {
	if m.lastAccess == nil {
		m.lastAccess = make(map[string]map[string]time.Time)
	}
	userLastAccess, exists := m.lastAccess[userName]
	if !exists {
		userLastAccess = make(map[string]time.Time)
		m.lastAccess[userName] = userLastAccess
	}
	userLastAccess[sessionID] = now
}

// evictIdleHelpersLocked lazily frees only helpers that have been idle for at
// least idleTTL. It runs while the manager lock is held, so a helper returned
// by GetOrCreateAIHelper is touched before another request can consider it
// stale. An in-flight response is always retained: model requests are bounded
// by the gateway timeout (90 seconds by default), far below the 30-minute idle
// TTL, and AIHelper tracks the full response lifetime.
func (m *AIHelperManager) evictIdleHelpersLocked(now time.Time) {
	if m.idleTTL <= 0 {
		return
	}

	for userName, userHelpers := range m.helpers {
		userLastAccess := m.lastAccess[userName]
		for sessionID, helper := range userHelpers {
			lastUsed, tracked := userLastAccess[sessionID]
			if tracked && now.Before(lastUsed.Add(m.idleTTL)) {
				continue
			}
			if helper != nil && helper.isHandlingResponse() {
				continue
			}

			delete(userHelpers, sessionID)
			if userLastAccess != nil {
				delete(userLastAccess, sessionID)
			}
		}
		if len(userHelpers) == 0 {
			delete(m.helpers, userName)
		}
		if len(userLastAccess) == 0 {
			delete(m.lastAccess, userName)
		}
	}
}

var globalManager *AIHelperManager
var once sync.Once

// GetGlobalManager gets the global manager instance.
func GetGlobalManager() *AIHelperManager {
	once.Do(func() {
		globalManager = NewAIHelperManager()
	})
	return globalManager
}
