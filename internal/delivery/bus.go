// Package delivery is outbound webhook delivery (spec 7): relay from the outbox
// to the bus, fan-out to endpoints, and a worker with signing, ordering,
// backoff, a circuit breaker, dead letters and replay.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Message is one record on the bus.
type Message struct {
	Key     string
	Value   []byte
	Headers map[string]string
}

// Bus is the durable fan-out between the outbox relay and the fan-out
// consumer. Kafka (Redpanda) in production, in memory in tests.
type Bus interface {
	Publish(ctx context.Context, m Message) error
	// Consume calls handle for every message, in order per key, until ctx ends.
	// A message is acknowledged only when handle returns nil; on error it is
	// retried, so handle must be idempotent (at-least-once).
	Consume(ctx context.Context, group string, handle func(context.Context, Message) error) error
}

// Envelope is the bus message body: one outbox row.
type Envelope struct {
	ID          int64           `json:"id"`
	WarehouseID string          `json:"warehouse_id"`
	Topic       string          `json:"topic"`
	Key         string          `json:"key"`
	Payload     json.RawMessage `json:"payload"`
	OccurredAt  time.Time       `json:"occurred_at"`
	Traceparent string          `json:"traceparent,omitempty"`
}

// ---------------------------------------------------------------------------
// In-memory bus (tests, single process)
// ---------------------------------------------------------------------------

// MemBus is a Bus backed by a slice. Every group sees every message.
type MemBus struct {
	mu   sync.Mutex
	msgs []Message
	wake chan struct{}
}

func NewMemBus() *MemBus { return &MemBus{wake: make(chan struct{}, 1)} }

func (b *MemBus) Publish(_ context.Context, m Message) error {
	b.mu.Lock()
	b.msgs = append(b.msgs, m)
	b.mu.Unlock()
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return nil
}

func (b *MemBus) Consume(ctx context.Context, _ string, handle func(context.Context, Message) error) error {
	next := 0
	for {
		b.mu.Lock()
		var m *Message
		if next < len(b.msgs) {
			m = &b.msgs[next]
		}
		b.mu.Unlock()
		if m == nil {
			select {
			case <-ctx.Done():
				return nil
			case <-b.wake:
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		if err := handle(ctx, *m); err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		next++
	}
}

// ---------------------------------------------------------------------------
// Kafka / Redpanda
// ---------------------------------------------------------------------------

// KafkaBus publishes to and consumes from one topic. The message key picks the
// partition, so events with the same ordering key stay in order.
type KafkaBus struct {
	brokers []string
	topic   string
	prod    *kgo.Client
}

// NewKafkaBus connects and creates the topic if it does not exist.
func NewKafkaBus(ctx context.Context, brokers []string, topic string, partitions int32) (*KafkaBus, error) {
	prod, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.DefaultProduceTopic(topic),
		kgo.RequiredAcks(kgo.AllISRAcks()), // idempotent producer is the franz-go default
	)
	if err != nil {
		return nil, fmt.Errorf("delivery: kafka client: %w", err)
	}
	adm := kadm.NewClient(prod)
	if _, err := adm.CreateTopic(ctx, partitions, 1, nil, topic); err != nil && !errors.Is(err, kerr.TopicAlreadyExists) {
		prod.Close()
		return nil, fmt.Errorf("delivery: create topic %q: %w", topic, err)
	}
	return &KafkaBus{brokers: brokers, topic: topic, prod: prod}, nil
}

func (b *KafkaBus) Close() { b.prod.Close() }

func (b *KafkaBus) Publish(ctx context.Context, m Message) error {
	rec := &kgo.Record{Key: []byte(m.Key), Value: m.Value}
	for k, v := range m.Headers {
		rec.Headers = append(rec.Headers, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}
	return b.prod.ProduceSync(ctx, rec).FirstErr()
}

func (b *KafkaBus) Consume(ctx context.Context, group string, handle func(context.Context, Message) error) error {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(b.brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(b.topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
	)
	if err != nil {
		return err
	}
	defer cl.Close()
	for ctx.Err() == nil {
		fetches := cl.PollFetches(ctx)
		if fetches.IsClientClosed() || ctx.Err() != nil {
			return nil
		}
		fetches.EachError(func(_ string, _ int32, err error) {
			if !errors.Is(err, context.Canceled) {
				time.Sleep(200 * time.Millisecond) // transient broker error: back off, keep polling
			}
		})
		var done []*kgo.Record
		fetches.EachRecord(func(r *kgo.Record) {
			m := Message{Key: string(r.Key), Value: r.Value, Headers: map[string]string{}}
			for _, h := range r.Headers {
				m.Headers[h.Key] = string(h.Value)
			}
			// Retry in place: committing past a failed record would lose it.
			for ctx.Err() == nil {
				if err := handle(ctx, m); err == nil {
					done = append(done, r)
					return
				}
				time.Sleep(200 * time.Millisecond)
			}
		})
		if len(done) > 0 {
			if err := cl.CommitRecords(ctx, done...); err != nil && ctx.Err() == nil {
				return fmt.Errorf("delivery: commit offsets: %w", err)
			}
		}
		cl.AllowRebalance()
	}
	return nil
}
