// Command migrate applies and verifies GopherAI's MySQL schema migrations.
//
// Examples:
//
//	go run ./cmd/migrate -command status
//	go run ./cmd/migrate -command up
//	go run ./cmd/migrate -command verify
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"GopherAI/common/mysql"
	"GopherAI/config"
)

func main() {
	command := flag.String("command", "up", "migration command: up, status, or verify")
	timeout := flag.Duration("timeout", 5*time.Minute, "maximum time for the migration command")
	flag.Parse()

	if *timeout <= 0 {
		log.Print("-timeout must be greater than zero")
		os.Exit(2)
	}
	if err := config.InitConfig(); err != nil {
		log.Printf("configuration is invalid: %v", err)
		os.Exit(1)
	}

	database, closeDB, err := mysql.Open()
	if err != nil {
		log.Printf("open MySQL: %v", err)
		os.Exit(1)
	}
	defer func() {
		if err := closeDB(); err != nil {
			log.Printf("close MySQL: %v", err)
		}
	}()

	baseCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(baseCtx, *timeout)
	defer cancel()

	switch strings.ToLower(strings.TrimSpace(*command)) {
	case "up":
		status, err := mysql.ApplyMigrations(ctx, database)
		if err != nil {
			log.Printf("apply migrations: %v", err)
			os.Exit(1)
		}
		printStatus(status)
	case "status":
		status, err := mysql.MigrationStatus(ctx, database)
		if err != nil {
			log.Printf("migration status: %v", err)
			os.Exit(1)
		}
		printStatus(status)
	case "verify":
		status, err := mysql.VerifyMigrations(ctx, database)
		if err != nil {
			log.Printf("verify migrations: %v", err)
			os.Exit(1)
		}
		printStatus(status)
	default:
		log.Printf("unsupported -command %q; use up, status, or verify", *command)
		os.Exit(2)
	}
}

func printStatus(status []mysql.MigrationInfo) {
	for _, item := range status {
		state := "pending"
		appliedAt := ""
		if item.Applied {
			state = "applied"
			if item.AppliedAt != nil {
				appliedAt = item.AppliedAt.UTC().Format(time.RFC3339)
			}
		}
		if appliedAt == "" {
			fmt.Printf("%s\t%s\t%s\n", item.Version, state, item.Name)
			continue
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", item.Version, state, item.Name, appliedAt)
	}
}
