package mcphub

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

const approvalTokenBytes = 32

type approvalTokenRecord struct {
	challengeID     string
	userName        string
	toolName        string
	argumentsDigest string
	expiresAt       time.Time
}

// ApprovalManager is an intentionally small in-memory approval store. Raw
// tokens are returned once and only their SHA-256 hashes are retained.
type ApprovalManager struct {
	mu         sync.Mutex
	ttl        time.Duration
	now        func() time.Time
	challenges map[string]*ApprovalChallenge
	tokens     map[string]approvalTokenRecord
}

func NewApprovalManager(ttl time.Duration) *ApprovalManager {
	if ttl <= 0 {
		ttl = defaultApprovalTTL
	}
	return &ApprovalManager{
		ttl:        ttl,
		now:        time.Now,
		challenges: make(map[string]*ApprovalChallenge),
		tokens:     make(map[string]approvalTokenRecord),
	}
}

func (manager *ApprovalManager) Create(userName string, tool ToolDefinition, arguments map[string]any) (*ApprovalChallenge, error) {
	if userName == "" {
		return nil, newError(ErrorApprovalInvalid, "approval requires an authenticated user", nil)
	}
	if !tool.RequiresApproval {
		return nil, newError(ErrorApprovalNotRequired, "this tool does not require approval", nil)
	}
	if err := tool.ValidateArguments(arguments); err != nil {
		return nil, err
	}
	digest, err := argumentsDigest(arguments)
	if err != nil {
		return nil, newError(ErrorInvalidArguments, "tool arguments cannot be approved", err)
	}
	now := manager.now().UTC()
	challenge := &ApprovalChallenge{
		ID:               uuid.NewString(),
		ToolName:         tool.Name,
		Risk:             tool.Risk,
		ArgumentsDigest:  digest,
		ArgumentsPreview: redactedArguments(arguments),
		ExpiresAt:        now.Add(manager.ttl),
		userName:         userName,
	}

	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.cleanupLocked(now)
	manager.challenges[challenge.ID] = challenge
	return cloneChallenge(challenge), nil
}

func (manager *ApprovalManager) Approve(userName, challengeID string) (*IssuedApproval, error) {
	now := manager.now().UTC()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.cleanupLocked(now)

	challenge, exists := manager.challenges[challengeID]
	if !exists || challenge.userName != userName || !now.Before(challenge.ExpiresAt) {
		return nil, newError(ErrorApprovalInvalid, "approval challenge is invalid or expired", nil)
	}
	if challenge.ApprovedAt != nil || challenge.ConsumedAt != nil {
		return nil, newError(ErrorApprovalInvalid, "approval challenge has already been used", nil)
	}

	rawToken := make([]byte, approvalTokenBytes)
	if _, err := rand.Read(rawToken); err != nil {
		return nil, newError(ErrorInternal, "approval token cannot be issued", err)
	}
	token := "mcp_apv_" + base64.RawURLEncoding.EncodeToString(rawToken)
	tokenHash := hashApprovalToken(token)
	approvedAt := now
	challenge.ApprovedAt = &approvedAt
	manager.tokens[tokenHash] = approvalTokenRecord{
		challengeID:     challenge.ID,
		userName:        userName,
		toolName:        challenge.ToolName,
		argumentsDigest: challenge.ArgumentsDigest,
		expiresAt:       challenge.ExpiresAt,
	}
	return &IssuedApproval{
		ChallengeID: challenge.ID,
		Token:       token,
		ExpiresAt:   challenge.ExpiresAt,
	}, nil
}

func (manager *ApprovalManager) Consume(userName, toolName string, arguments map[string]any, token string) error {
	if token == "" {
		return newError(ErrorApprovalRequired, "an approval token is required", nil)
	}
	digest, err := argumentsDigest(arguments)
	if err != nil {
		return newError(ErrorApprovalInvalid, "approval token cannot be validated", err)
	}
	now := manager.now().UTC()
	tokenHash := hashApprovalToken(token)

	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.cleanupLocked(now)
	record, exists := manager.tokens[tokenHash]
	if !exists {
		return newError(ErrorApprovalInvalid, "approval token is invalid or has already been used", nil)
	}
	// Consume a recognized token even if binding checks fail. This prevents a
	// token from being probed against multiple argument sets.
	delete(manager.tokens, tokenHash)
	if challenge := manager.challenges[record.challengeID]; challenge != nil {
		consumedAt := now
		challenge.ConsumedAt = &consumedAt
	}

	valid := now.Before(record.expiresAt) &&
		record.userName == userName &&
		record.toolName == toolName &&
		constantStringEqual(record.argumentsDigest, digest)
	if !valid {
		return newError(ErrorApprovalInvalid, "approval token does not match this invocation", nil)
	}
	return nil
}

func (manager *ApprovalManager) cleanupLocked(now time.Time) {
	for tokenHash, record := range manager.tokens {
		if !now.Before(record.expiresAt) {
			delete(manager.tokens, tokenHash)
		}
	}
	for id, challenge := range manager.challenges {
		retentionEnd := challenge.ExpiresAt.Add(manager.ttl)
		if !now.Before(retentionEnd) {
			delete(manager.challenges, id)
		}
	}
}

func hashApprovalToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func constantStringEqual(left, right string) bool {
	return hmac.Equal([]byte(left), []byte(right))
}

func cloneChallenge(source *ApprovalChallenge) *ApprovalChallenge {
	if source == nil {
		return nil
	}
	clone := *source
	clone.ArgumentsPreview = append(clone.ArgumentsPreview[:0:0], source.ArgumentsPreview...)
	clone.userName = ""
	return &clone
}

func (manager *ApprovalManager) String() string {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return fmt.Sprintf("ApprovalManager(challenges=%d,tokens=%d)", len(manager.challenges), len(manager.tokens))
}
