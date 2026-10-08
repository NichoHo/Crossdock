package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/nicholasgasior/crossdock/internal/outbox"
	"github.com/nicholasgasior/crossdock/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Relay moves outbox rows to the bus until ctx ends. Rows leave the outbox only
// after the bus accepted them, so delivery to the bus is at least once.
func Relay(ctx context.Context, db *sql.DB, bus Bus, every time.Duration, log *slog.Logger) {
	outbox.Run(ctx, db, every, func(e outbox.Event) error { return publish(ctx, bus, e) },
		func(err error) { log.Error("outbox relay", "err", err) })
}

// RelayOnce relays one batch; tests call it directly.
func RelayOnce(ctx context.Context, db *sql.DB, bus Bus, limit int) (int, error) {
	return outbox.RelayOnce(ctx, db, limit, func(e outbox.Event) error { return publish(ctx, bus, e) })
}

func publish(ctx context.Context, bus Bus, e outbox.Event) error {
	// The relay span continues the trace of the request that wrote the row.
	ctx, span := telemetry.Tracer().Start(telemetry.WithTraceparent(ctx, e.Traceparent), "outbox.relay",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(attribute.String("event.topic", e.Topic), attribute.Int64("event.sequence", e.ID)))
	defer span.End()

	body, err := json.Marshal(Envelope{ID: e.ID, WarehouseID: e.WarehouseID, Topic: e.Topic, Key: e.Key,
		Payload: e.Payload, OccurredAt: e.CreatedAt, Traceparent: telemetry.Traceparent(ctx)})
	if err != nil {
		return err
	}
	key := e.Key
	if key == "" {
		key = strconv.FormatInt(e.ID, 10) // unordered events spread across partitions
	}
	if err := bus.Publish(ctx, Message{Key: key, Value: body, Headers: map[string]string{"traceparent": telemetry.Traceparent(ctx)}}); err != nil {
		span.RecordError(err)
		return fmt.Errorf("publish event %d: %w", e.ID, err)
	}
	return nil
}
