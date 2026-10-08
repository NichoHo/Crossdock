// Package outbox implements the transactional outbox (spec 5.2). State changes
// write a row in the same transaction; Relay delivers rows at least once to
// in-process consumers. Phase 2 swaps the relay target for the bus.
package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nicholasgasior/crossdock/internal/telemetry"
)

// Execer is satisfied by *sql.DB and *sql.Tx.
type Execer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// Emit writes an outbox row. Call it with the same *sql.Tx as the state change.
func Emit(ctx context.Context, x Execer, warehouseID, topic, key string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outbox: marshal %s: %w", topic, err)
	}
	_, err = x.ExecContext(ctx,
		`INSERT INTO outbox (warehouse_id, topic, key, payload, traceparent) VALUES ($1,$2,$3,$4,$5)`,
		warehouseID, topic, key, b, telemetry.Traceparent(ctx))
	if err != nil {
		return fmt.Errorf("outbox: insert %s: %w", topic, err)
	}
	return nil
}

// Event is one outbox row handed to a consumer.
type Event struct {
	ID          int64
	WarehouseID string
	Topic       string
	Key         string
	Payload     json.RawMessage
	CreatedAt   time.Time
	// Traceparent is the W3C trace context of the request that caused the event.
	Traceparent string
}

// RelayOnce delivers up to limit pending rows in id order and marks them
// relayed. If handle returns an error the batch rolls back and is retried, so
// delivery is at least once: handlers must be idempotent on Event.ID.
func RelayOnce(ctx context.Context, db *sql.DB, limit int, handle func(Event) error) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	rows, err := tx.QueryContext(ctx, `
		SELECT id, warehouse_id, topic, key, payload, created_at, traceparent
		FROM outbox WHERE relayed_at IS NULL
		ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return 0, err
	}
	var evs []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.WarehouseID, &e.Topic, &e.Key, &e.Payload, &e.CreatedAt, &e.Traceparent); err != nil {
			rows.Close()
			return 0, err
		}
		evs = append(evs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, e := range evs {
		if err := handle(e); err != nil {
			return 0, fmt.Errorf("outbox: handle event %d (%s): %w", e.ID, e.Topic, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE outbox SET relayed_at = NOW() WHERE id = $1`, e.ID); err != nil {
			return 0, err
		}
	}
	return len(evs), tx.Commit()
}

// Run polls RelayOnce until ctx is done.
func Run(ctx context.Context, db *sql.DB, every time.Duration, handle func(Event) error, onErr func(error)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		for {
			n, err := RelayOnce(ctx, db, 100, handle)
			if err != nil {
				if ctx.Err() == nil && onErr != nil {
					onErr(err)
				}
				break
			}
			if n == 0 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
