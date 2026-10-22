// Package testdb provides a test helper that opens a real PostgreSQL
// connection for integration tests. It applies all migrations to a fresh
// schema before returning the connection, and tears it down when the test ends.
//
// Connection string: The test database URL is read from the environment
// variable CROSSDOCK_TEST_DB_URL. If the variable is absent, the test is
// skipped.
//
// Schema isolation: Each call to Open creates a unique PostgreSQL schema
// (named by the test name + a random suffix), runs migrations inside it,
// and drops the schema in t.Cleanup. This means tests can run in parallel
// against the same database without interfering.
package testdb

import (
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/lib/pq"
)

// Open connects to the test database, creates an isolated schema for the test,
// applies all migrations, and returns the *sql.DB. The schema is dropped in
// t.Cleanup.
//
// If CROSSDOCK_TEST_DB_URL is not set, the test is skipped.
func Open(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("CROSSDOCK_TEST_DB_URL")
	if url == "" {
		t.Skip("CROSSDOCK_TEST_DB_URL not set — skipping integration test")
	}

	// Create a unique schema for this test.
	schemaName := schemaForTest(t)

	// Open a root connection to create the schema.
	rootDB, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatalf("testdb: open root connection: %v", err)
	}
	if err := rootDB.Ping(); err != nil {
		rootDB.Close()
		t.Fatalf("testdb: ping root connection: %v", err)
	}

	if _, err := rootDB.Exec(fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %q`, schemaName)); err != nil {
		rootDB.Close()
		t.Fatalf("testdb: create schema %q: %v", schemaName, err)
	}
	rootDB.Close()

	// Open a schema-scoped connection.
	schemaURL := addSearchPath(url, schemaName)
	db, err := sql.Open("postgres", schemaURL)
	if err != nil {
		t.Fatalf("testdb: open schema connection: %v", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatalf("testdb: ping schema connection: %v", err)
	}

	// Apply migrations.
	if err := applyMigrations(db, schemaName, url); err != nil {
		db.Close()
		t.Fatalf("testdb: apply migrations: %v", err)
	}

	// Drop schema on test cleanup.
	t.Cleanup(func() {
		db.Close()
		rootDB2, err := sql.Open("postgres", url)
		if err == nil {
			rootDB2.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schemaName)) //nolint:errcheck
			rootDB2.Close()
		}
	})

	return db
}

// Chaos opens a second pool on db's schema, tagged with its own application_name,
// and returns it with a kill function that terminates one random backend of that pool
// (what a PostgreSQL crash or a network cut looks like to the client). Backends of
// other tests and of db itself are untouched.
func Chaos(t *testing.T, db *sql.DB) (victim *sql.DB, kill func() int) {
	t.Helper()
	var schema string
	if err := db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatalf("testdb: current_schema: %v", err)
	}
	app := fmt.Sprintf("chaos_%08x", rand.Uint32())
	victim, err := sql.Open("postgres", addSearchPath(os.Getenv("CROSSDOCK_TEST_DB_URL"), schema)+"&application_name="+app)
	if err != nil {
		t.Fatalf("testdb: open chaos pool: %v", err)
	}
	t.Cleanup(func() { victim.Close() })
	return victim, func() int {
		var n int
		_ = db.QueryRow(`SELECT count(pg_terminate_backend(pid)) FROM (SELECT pid FROM pg_stat_activity WHERE application_name = $1 ORDER BY random() LIMIT 1) b`, app).Scan(&n)
		return n
	}
}

// schemaForTest generates a deterministic but unique schema name for the test.
func schemaForTest(t *testing.T) string {
	t.Helper()
	// Sanitise the test name to valid PostgreSQL identifier chars.
	name := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32 // lower-case
		}
		return '_'
	}, t.Name())
	// PostgreSQL identifier max length is 63 bytes. We need room for the suffix.
	if len(name) > 45 {
		name = name[:45]
	}
	return fmt.Sprintf("crossdock_%s_%08x", name, rand.Uint32())
}

// addSearchPath appends search_path=schemaName to the DSN.
func addSearchPath(dsn, schemaName string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&search_path=" + schemaName
	}
	return dsn + "?search_path=" + schemaName
}

// migrationsDir returns the absolute path to the migrations directory.
// It locates it relative to this file using runtime.Caller.
func migrationsDir() string {
	_, thisFile, _, _ := runtime.Caller(0)
	// thisFile is .../internal/testdb/testdb.go
	// migrations is at the project root: .../migrations/
	projectRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return filepath.Join(projectRoot, "migrations")
}

// applyMigrations runs all migrations against the scoped database connection.
func applyMigrations(db *sql.DB, schemaName, rootURL string) error {
	driver, err := postgres.WithInstance(db, &postgres.Config{
		SchemaName:        schemaName,
		MigrationsTable:   "schema_migrations",
	})
	if err != nil {
		return fmt.Errorf("testdb: create migrate driver: %w", err)
	}

	migrDir := migrationsDir()
	// On Windows, file:// paths need forward slashes.
	migrDir = filepath.ToSlash(migrDir)

	m, err := migrate.NewWithDatabaseInstance(
		"file://"+migrDir,
		"postgres",
		driver,
	)
	if err != nil {
		return fmt.Errorf("testdb: create migrate instance: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("testdb: migrate up: %w", err)
	}
	return nil
}
