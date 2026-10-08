package main

import (
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/lib/pq"

	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
)

// runMigrations applies or rolls back migrations using golang-migrate.
func runMigrations(dbURL, direction string) error {
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return fmt.Errorf("migrate: open db: %w", err)
	}
	defer db.Close()

	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		return fmt.Errorf("migrate: create driver: %w", err)
	}

	migrDir := migrationsPath()
	m, err := migrate.NewWithDatabaseInstance(
		"file://"+filepath.ToSlash(migrDir),
		"postgres",
		driver,
	)
	if err != nil {
		return fmt.Errorf("migrate: create instance: %w", err)
	}

	switch direction {
	case "up":
		if err := m.Up(); err != nil && err != migrate.ErrNoChange {
			return fmt.Errorf("migrate up: %w", err)
		}
		slog.Info("migrations applied")
	case "down":
		if err := m.Steps(-1); err != nil && err != migrate.ErrNoChange {
			return fmt.Errorf("migrate down: %w", err)
		}
		slog.Info("one migration rolled back")
	default:
		return fmt.Errorf("migrate: unknown direction %q (use up or down)", direction)
	}
	return nil
}

// migrationsPath returns the path to the migrations directory. In production
// builds the binary ships with the migrations embedded; for development we
// locate them relative to the source tree.
func migrationsPath() string {
	// First try an environment override (useful in Docker).
	if env := os.Getenv("CROSSDOCK_MIGRATIONS_DIR"); env != "" {
		return env
	}
	// Locate relative to the calling source file.
	_, f, _, _ := runtime.Caller(0)
	// f = .../cmd/crossdock/migrate.go → ../../migrations
	return filepath.Join(filepath.Dir(f), "..", "..", "migrations")
}
