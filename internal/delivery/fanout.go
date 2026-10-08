package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// FanoutGroup is the consumer group of the fan-out consumer.
const FanoutGroup = "crossdock-fanout"

// RunFanout consumes the bus until ctx ends, turning each event into one
// pending delivery per matching endpoint.
func RunFanout(ctx context.Context, db *sql.DB, bus Bus) error {
	return bus.Consume(ctx, FanoutGroup, func(ctx context.Context, m Message) error { return Fanout(ctx, db, m) })
}

// Fanout stores the event and creates a delivery for every active endpoint of
// its warehouse whose filter matches. Both inserts are idempotent, so a message
// seen twice (at-least-once bus) changes nothing the second time.
func Fanout(ctx context.Context, db *sql.DB, m Message) error {
	var env Envelope
	if err := json.Unmarshal(m.Value, &env); err != nil {
		return fmt.Errorf("fanout: bad message: %w", err) // poison message: surfaces in logs, retried
	}
	ctx, span := telemetry.Tracer().Start(telemetry.WithTraceparent(ctx, firstNonEmpty(m.Headers["traceparent"], env.Traceparent)),
		"delivery.fanout", trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attribute.String("event.topic", env.Topic), attribute.Int64("event.sequence", env.ID)))
	defer span.End()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO events (id, warehouse_id, topic, key, payload, occurred_at, traceparent)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (id) DO NOTHING`,
		env.ID, env.WarehouseID, env.Topic, env.Key, []byte(env.Payload), env.OccurredAt, telemetry.Traceparent(ctx)); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, events FROM endpoints WHERE warehouse_id=$1 AND state='active'`, env.WarehouseID)
	if err != nil {
		return err
	}
	var targets []string
	for rows.Next() {
		var id string
		var filter pq.StringArray
		if err := rows.Scan(&id, &filter); err != nil {
			rows.Close()
			return err
		}
		if Matches(filter, env.Topic) {
			targets = append(targets, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(targets) > 0 {
		// One statement for all endpoints: a round trip per endpoint made fan-out
		// the bottleneck at 100 endpoints.
		dids := make([]string, len(targets))
		for i := range targets {
			dids[i] = ids.New("dlv")
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO deliveries (id, event_id, warehouse_id, endpoint_id, ordering_key)
			SELECT t.id, $3, $4, t.endpoint_id, $5 FROM unnest($1::text[], $2::text[]) AS t(id, endpoint_id)
			ON CONFLICT (event_id, endpoint_id) WHERE replay_of = '' DO NOTHING`,
			pq.Array(dids), pq.Array(targets), env.ID, env.WarehouseID, env.Key); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
