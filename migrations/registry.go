// Package migrations contains the ordered, executable database schema history.
// It deliberately uses only GORM and the project's existing MySQL driver, so
// applying a release does not require downloading a separate migration binary.
package migrations

import (
	"context"
	"crypto/sha256"
	"fmt"

	"gorm.io/gorm"
)

// Definition describes one irreversible forward migration. New schema work
// must add a definition with a larger Version; never edit a released one.
type Definition struct {
	Version  string
	Name     string
	Revision string
	Up       func(context.Context, *gorm.DB) error
}

// Checksum detects accidental edits to released migration definitions. Bump
// neither Revision nor Version after the migration has been applied anywhere;
// create a new migration instead.
func (m Definition) Checksum() string {
	sum := sha256.Sum256([]byte(m.Version + "\n" + m.Name + "\n" + m.Revision))
	return fmt.Sprintf("%x", sum[:])
}

// All is kept in version order to make review and deployment state explicit.
func All() []Definition {
	return []Definition{
		{
			Version:  "202607290001",
			Name:     "legacy_gorm_schema_baseline",
			Revision: "v1",
			Up:       legacyGORMBaseline,
		},
		{
			Version:  "202607290002",
			Name:     "messages_message_id_backfill",
			Revision: "v1",
			Up:       messagesMessageIDBackfill,
		},
		{
			Version:  "202607290003",
			Name:     "sessions_activity_pagination_index",
			Revision: "v1",
			Up:       sessionsActivityPaginationIndex,
		},
		{
			Version:  "202608040001",
			Name:     "agent_step_operation_id",
			Revision: "v1",
			Up:       agentStepOperationID,
		},
	}
}
