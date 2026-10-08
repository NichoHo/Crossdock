// crossdock is the main service binary and CLI for the Crossdock warehouse
// management platform.
//
// Usage:
//
//	crossdock serve         Start the API server (Phase 1+)
//	crossdock rebuild positions  Truncate and replay the positions projection
//	crossdock generate [name] [warehouse-id]   Populate the database with synthetic data
//	crossdock migrate up    Apply database migrations
//	crossdock migrate down  Roll back one migration
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"

	_ "github.com/lib/pq"
	"github.com/nicholasgasior/crossdock/internal/generate"
	"github.com/nicholasgasior/crossdock/internal/inventory"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "crossdock: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: crossdock <command> [args]\n\nCommands:\n  rebuild positions\n  generate\n  migrate up|down")
	}

	switch args[0] {
	case "serve":
		return cmdServe()

	case "synthetic":
		return cmdSynthetic(args[1:])

	case "slotting":
		return cmdSlotting(args[1:])

	case "courier":
		return cmdCourier(args[1:])

	case "remittance":
		return cmdRemittance(args[1:])

	case "rebuild":
		if len(args) < 2 || args[1] != "positions" {
			return fmt.Errorf("usage: crossdock rebuild positions")
		}
		return cmdRebuildPositions()

	case "generate":
		return cmdGenerate(args[1:])

	case "migrate":
		if len(args) < 2 {
			return fmt.Errorf("usage: crossdock migrate up|down")
		}
		return cmdMigrate(args[1])

	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// ---------------------------------------------------------------------------
// rebuild positions
// ---------------------------------------------------------------------------

func cmdRebuildPositions() error {
	dbURL := requireEnv("CROSSDOCK_DATABASE_URL")
	warehouseID := requireEnv("CROSSDOCK_WAREHOUSE_ID")

	db, err := openDB(dbURL)
	if err != nil {
		return err
	}
	defer db.Close()

	store := inventory.NewStore(db)
	ctx := context.Background()

	slog.Info("rebuilding positions", "warehouse_id", warehouseID)
	start := time.Now()

	if err := store.RebuildPositions(ctx, inventory.WarehouseID(warehouseID)); err != nil {
		return fmt.Errorf("rebuild positions: %w", err)
	}

	slog.Info("rebuild complete", "warehouse_id", warehouseID, "elapsed", time.Since(start))
	return nil
}

// ---------------------------------------------------------------------------
// generate
// ---------------------------------------------------------------------------

func cmdGenerate(args []string) error {
	if len(args) > 0 && args[0] == "history" {
		return cmdGenerateHistory(args[1:])
	}
	dbURL := requireEnv("CROSSDOCK_DATABASE_URL")

	db, err := openDB(dbURL)
	if err != nil {
		return err
	}
	defer db.Close()

	cfg := generate.DefaultConfig()
	// Allow override of warehouse name from args.
	if len(args) > 0 {
		cfg.WarehouseName = args[0]
	}
	if len(args) > 1 {
		cfg.WarehouseID = args[1] // generate <name> <warehouse-id>
	}

	store := inventory.NewStore(db)
	ctx := context.Background()

	slog.Info("generating synthetic data", "warehouse_name", cfg.WarehouseName, "skus", cfg.SKUCount, "orders", cfg.OrderCount)
	start := time.Now()

	result, err := generate.Run(ctx, db, store, cfg)
	if err != nil {
		return fmt.Errorf("generate: %w", err)
	}

	slog.Info("generation complete",
		"warehouse_id", result.Warehouse.ID,
		"locations", len(result.Locations),
		"skus", len(result.SKUs),
		"movements", result.MovementCount,
		"elapsed", time.Since(start),
	)
	return nil
}

// ---------------------------------------------------------------------------
// migrate
// ---------------------------------------------------------------------------

func cmdMigrate(direction string) error {
	dbURL := requireEnv("CROSSDOCK_DATABASE_URL")

	// We use golang-migrate directly. This avoids requiring a separate migrate
	// binary and keeps the migration command co-located with the service.
	return runMigrations(dbURL, direction)
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func openDB(url string) (*sql.DB, error) {
	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25) // equal to max open: workers must not churn connections
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "crossdock: required environment variable %s is not set\n", key)
		os.Exit(1)
	}
	return v
}
