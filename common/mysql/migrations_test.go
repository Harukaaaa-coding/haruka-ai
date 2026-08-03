package mysql

import (
	"context"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestMigrationStartupModeFromEnvironment(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want MigrationStartupMode
		err  string
	}{
		{
			name: "local defaults to verify",
			want: MigrationStartupVerify,
		},
		{
			name: "production defaults to verify",
			env:  map[string]string{"GOPHERAI_ENV": "production"},
			want: MigrationStartupVerify,
		},
		{
			name: "production cannot opt into compatibility startup writes",
			env:  map[string]string{"GOPHERAI_ENV": "production", "GOPHERAI_DB_MIGRATION_MODE": "compatibility"},
			err:  "not allowed",
		},
		{
			name: "manual is a verify alias",
			env:  map[string]string{"GOPHERAI_DB_MIGRATION_MODE": "manual"},
			want: MigrationStartupVerify,
		},
		{
			name: "invalid mode is rejected",
			env:  map[string]string{"GOPHERAI_DB_MIGRATION_MODE": "unsafe"},
			err:  "GOPHERAI_DB_MIGRATION_MODE",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode, err := migrationStartupModeFromEnvironment(func(name string) string { return test.env[name] })
			if test.err != "" {
				if err == nil || !strings.Contains(err.Error(), test.err) {
					t.Fatalf("migrationStartupModeFromEnvironment() error = %v, want %q", err, test.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("migrationStartupModeFromEnvironment(): %v", err)
			}
			if mode != test.want {
				t.Fatalf("mode = %q, want %q", mode, test.want)
			}
		})
	}
}

func TestValidateMigrations(t *testing.T) {
	validUp := func(context.Context, *gorm.DB) error { return nil }
	tests := []struct {
		name string
		list []migration
		want string
	}{
		{
			name: "registered migrations are valid",
			list: registeredMigrations(),
		},
		{
			name: "duplicate version",
			list: []migration{
				{Version: "1", Name: "first", Up: validUp},
				{Version: "1", Name: "second", Up: validUp},
			},
			want: "duplicate",
		},
		{
			name: "out of order",
			list: []migration{
				{Version: "2", Name: "second", Up: validUp},
				{Version: "1", Name: "first", Up: validUp},
			},
			want: "ordered",
		},
		{
			name: "missing definition",
			list: []migration{{Version: "1", Name: "", Up: validUp}},
			want: "incomplete",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateMigrations(test.list)
			if test.want == "" {
				if err != nil {
					t.Fatalf("validateMigrations(): %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateMigrations() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestMigrationChecksumsAreStableAndDistinct(t *testing.T) {
	seen := make(map[string]struct{})
	for _, definition := range registeredMigrations() {
		checksum := definition.Checksum()
		if len(checksum) != 64 {
			t.Fatalf("checksum length for %s = %d, want 64", definition.Version, len(checksum))
		}
		if _, exists := seen[checksum]; exists {
			t.Fatalf("duplicate checksum %q", checksum)
		}
		seen[checksum] = struct{}{}
		if checksum != definition.Checksum() {
			t.Fatalf("checksum for %s is not deterministic", definition.Version)
		}
	}
}
