package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	schemamigrations "GopherAI/migrations"
	"gorm.io/gorm"
)

// The application schema is changed only through this migration list. The
// initial migration deliberately uses the existing GORM models as a one-time
// bootstrap for installations created before this runner existed. It is run by
// cmd/migrate (or the explicitly enabled local compatibility mode), never as
// an unconditional side effect of starting the API.
const (
	migrationTableName = "gopherai_schema_migrations"
	migrationLockName  = "gopherai:schema-migrations"
	migrationLockWait  = 30
)

var (
	// ErrMigrationHistoryMissing means migrations have not been initialized for
	// this database yet. Run `go run ./cmd/migrate -command up` before starting
	// an API instance in verify mode.
	ErrMigrationHistoryMissing = errors.New("database migration history is missing")
	// ErrPendingMigrations means the database is older than this binary.
	ErrPendingMigrations = errors.New("database has pending migrations")
)

// MigrationStartupMode controls whether API startup may make schema changes.
// Compatibility is intended only for a time-limited local transition from the
// legacy application-start AutoMigrate behaviour. API startup otherwise uses
// Verify and fails closed until an explicit deployment migration completes.
type MigrationStartupMode string

const (
	MigrationStartupCompatibility MigrationStartupMode = "compatibility"
	MigrationStartupVerify        MigrationStartupMode = "verify"
)

type migration = schemamigrations.Definition

// MigrationInfo is safe to print in deployment logs. It intentionally does
// not contain connection information or SQL text.
type MigrationInfo struct {
	Version   string
	Name      string
	Checksum  string
	Applied   bool
	AppliedAt *time.Time
}

type appliedMigration struct {
	Version   string     `gorm:"column:version"`
	Name      string     `gorm:"column:name"`
	Checksum  string     `gorm:"column:checksum"`
	AppliedAt *time.Time `gorm:"column:applied_at"`
}

func registeredMigrations() []migration {
	return schemamigrations.All()
}

func validateMigrations(list []migration) error {
	if len(list) == 0 {
		return errors.New("no database migrations are registered")
	}
	seen := make(map[string]struct{}, len(list))
	previous := ""
	for _, migration := range list {
		if strings.TrimSpace(migration.Version) == "" || strings.TrimSpace(migration.Name) == "" || migration.Up == nil {
			return errors.New("database migration has an incomplete definition")
		}
		if _, exists := seen[migration.Version]; exists {
			return fmt.Errorf("duplicate database migration version %q", migration.Version)
		}
		if previous != "" && migration.Version <= previous {
			return errors.New("database migrations are not ordered by version")
		}
		seen[migration.Version] = struct{}{}
		previous = migration.Version
	}
	return nil
}

// ApplyMigrations runs all missing migrations under a MySQL advisory lock. It
// is safe to call repeatedly: a migration is recorded only after its Up
// function succeeds, and every migration in this repository is restart-safe.
func ApplyMigrations(ctx context.Context, db *gorm.DB) ([]MigrationInfo, error) {
	if db == nil {
		return nil, errors.New("mysql database is nil")
	}
	list := registeredMigrations()
	if err := validateMigrations(list); err != nil {
		return nil, err
	}

	var status []MigrationInfo
	err := withMigrationLock(ctx, db, func(connection *gorm.DB) error {
		if err := ensureMigrationTable(connection); err != nil {
			return err
		}
		applied, err := loadAppliedMigrations(connection)
		if err != nil {
			return err
		}
		if err := verifyRecordedChecksums(list, applied); err != nil {
			return err
		}

		for _, definition := range list {
			if _, exists := applied[definition.Version]; exists {
				continue
			}
			if err := definition.Up(ctx, connection); err != nil {
				return fmt.Errorf("apply database migration %s (%s): %w", definition.Version, definition.Name, err)
			}
			if err := connection.WithContext(ctx).Exec(
				"INSERT INTO "+migrationTableName+" (version, name, checksum) VALUES (?, ?, ?)",
				definition.Version,
				definition.Name,
				definition.Checksum(),
			).Error; err != nil {
				return fmt.Errorf("record database migration %s: %w", definition.Version, err)
			}
			applied[definition.Version] = appliedMigration{
				Version:  definition.Version,
				Name:     definition.Name,
				Checksum: definition.Checksum(),
			}
		}

		var statusErr error
		status, statusErr = migrationStatus(connection, list, applied)
		return statusErr
	})
	return status, err
}

// VerifyMigrations performs no DDL. It fails if the migration history is
// absent, a recorded migration was altered, or this binary has unapplied work.
func VerifyMigrations(ctx context.Context, db *gorm.DB) ([]MigrationInfo, error) {
	if db == nil {
		return nil, errors.New("mysql database is nil")
	}
	list := registeredMigrations()
	if err := validateMigrations(list); err != nil {
		return nil, err
	}

	var status []MigrationInfo
	err := withMigrationLock(ctx, db, func(connection *gorm.DB) error {
		exists, err := migrationTableExists(connection)
		if err != nil {
			return err
		}
		if !exists {
			return ErrMigrationHistoryMissing
		}
		applied, err := loadAppliedMigrations(connection)
		if err != nil {
			return err
		}
		if err := verifyRecordedChecksums(list, applied); err != nil {
			return err
		}
		status, err = migrationStatus(connection, list, applied)
		if err != nil {
			return err
		}
		for _, item := range status {
			if !item.Applied {
				return fmt.Errorf("%w: %s (%s)", ErrPendingMigrations, item.Version, item.Name)
			}
		}
		return nil
	})
	return status, err
}

// MigrationStatus returns the known state without applying schema changes.
// Unlike VerifyMigrations, an uninitialized database is represented as an
// error so operators cannot mistake it for an up-to-date database.
func MigrationStatus(ctx context.Context, db *gorm.DB) ([]MigrationInfo, error) {
	if db == nil {
		return nil, errors.New("mysql database is nil")
	}
	list := registeredMigrations()
	if err := validateMigrations(list); err != nil {
		return nil, err
	}

	var status []MigrationInfo
	err := db.WithContext(ctx).Connection(func(connection *gorm.DB) error {
		exists, err := migrationTableExists(connection)
		if err != nil {
			return err
		}
		if !exists {
			return ErrMigrationHistoryMissing
		}
		applied, err := loadAppliedMigrations(connection)
		if err != nil {
			return err
		}
		if err := verifyRecordedChecksums(list, applied); err != nil {
			return err
		}
		status, err = migrationStatus(connection, list, applied)
		return err
	})
	return status, err
}

func migrationStatus(_ *gorm.DB, list []migration, applied map[string]appliedMigration) ([]MigrationInfo, error) {
	status := make([]MigrationInfo, 0, len(list))
	for _, definition := range list {
		item := MigrationInfo{
			Version:  definition.Version,
			Name:     definition.Name,
			Checksum: definition.Checksum(),
		}
		if recorded, exists := applied[definition.Version]; exists {
			item.Applied = true
			item.AppliedAt = recorded.AppliedAt
		}
		status = append(status, item)
	}
	return status, nil
}

func withMigrationLock(ctx context.Context, db *gorm.DB, run func(*gorm.DB) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return db.WithContext(ctx).Connection(func(connection *gorm.DB) error {
		var acquired sql.NullInt64
		if err := connection.WithContext(ctx).Raw("SELECT GET_LOCK(?, ?)", migrationLockName, migrationLockWait).Scan(&acquired).Error; err != nil {
			return fmt.Errorf("acquire database migration lock: %w", err)
		}
		if !acquired.Valid || acquired.Int64 != 1 {
			return errors.New("timed out waiting for database migration lock")
		}
		defer func() {
			// The connection-scoped lock is also released if this process dies.
			// Do not replace the migration result with a best-effort unlock error.
			_ = connection.WithContext(context.Background()).Exec("SELECT RELEASE_LOCK(?)", migrationLockName).Error
		}()
		return run(connection)
	})
}

func ensureMigrationTable(db *gorm.DB) error {
	return db.Exec(`CREATE TABLE IF NOT EXISTS gopherai_schema_migrations (
  version VARCHAR(128) NOT NULL,
  name VARCHAR(255) NOT NULL,
  checksum CHAR(64) NOT NULL,
  applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`).Error
}

func migrationTableExists(db *gorm.DB) (bool, error) {
	var count int64
	err := db.Raw(
		"SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?",
		migrationTableName,
	).Scan(&count).Error
	return count > 0, err
}

func loadAppliedMigrations(db *gorm.DB) (map[string]appliedMigration, error) {
	var recorded []appliedMigration
	if err := db.Raw("SELECT version, name, checksum, applied_at FROM " + migrationTableName + " ORDER BY version").Scan(&recorded).Error; err != nil {
		return nil, err
	}
	applied := make(map[string]appliedMigration, len(recorded))
	for _, item := range recorded {
		if _, exists := applied[item.Version]; exists {
			return nil, fmt.Errorf("database migration history contains duplicate version %q", item.Version)
		}
		applied[item.Version] = item
	}
	return applied, nil
}

func verifyRecordedChecksums(list []migration, applied map[string]appliedMigration) error {
	known := make(map[string]migration, len(list))
	for _, definition := range list {
		known[definition.Version] = definition
	}
	for version, recorded := range applied {
		definition, exists := known[version]
		if !exists {
			return fmt.Errorf("database migration history contains unknown version %q", version)
		}
		if recorded.Name != definition.Name || recorded.Checksum != definition.Checksum() {
			return fmt.Errorf("database migration history does not match source for version %s", version)
		}
	}
	return nil
}

func migrationStartupModeFromEnvironment(lookup func(string) string) (MigrationStartupMode, error) {
	environment := strings.ToLower(strings.TrimSpace(lookup("GOPHERAI_ENV")))
	production := false
	switch environment {
	case "production", "prod", "staging", "stage":
		production = true
	}
	raw := strings.ToLower(strings.TrimSpace(lookup("GOPHERAI_DB_MIGRATION_MODE")))
	if raw == "" {
		return MigrationStartupVerify, nil
	}
	switch raw {
	case "compatibility", "auto":
		if production {
			return "", errors.New("GOPHERAI_DB_MIGRATION_MODE=compatibility is not allowed when GOPHERAI_ENV is production or staging")
		}
		return MigrationStartupCompatibility, nil
	case "verify", "manual":
		return MigrationStartupVerify, nil
	default:
		return "", errors.New("GOPHERAI_DB_MIGRATION_MODE must be compatibility or verify")
	}
}

func runStartupMigrationPolicy(db *gorm.DB) error {
	mode, err := migrationStartupModeFromEnvironment(os.Getenv)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	switch mode {
	case MigrationStartupCompatibility:
		if _, err := ApplyMigrations(ctx, db); err != nil {
			return err
		}
		return nil
	case MigrationStartupVerify:
		if _, err := VerifyMigrations(ctx, db); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("unsupported database migration startup mode %q", mode)
	}
}
