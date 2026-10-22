package delivery_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicholasgasior/crossdock/internal/delivery"
)

// chaosBus is a MemBus that can be taken down, as Redpanda would be when killed:
// while down, Publish fails and consumed messages are not acknowledged.
type chaosBus struct {
	*delivery.MemBus
	down      atomic.Bool
	failEvery int64 // when > 0, every Nth Publish fails (a relay killed mid-batch)
	calls     atomic.Int64

	mu        sync.Mutex
	published map[int64]int // outbox id -> times the bus accepted it
}

var errBusDown = errors.New("bus down")

func newChaosBus() *chaosBus {
	return &chaosBus{MemBus: delivery.NewMemBus(), published: map[int64]int{}}
}

func (b *chaosBus) Publish(ctx context.Context, m delivery.Message) error {
	if b.down.Load() || (b.failEvery > 0 && b.calls.Add(1)%b.failEvery == 0) {
		return errBusDown
	}
	var env delivery.Envelope
	if err := json.Unmarshal(m.Value, &env); err != nil {
		return err
	}
	if err := b.MemBus.Publish(ctx, m); err != nil {
		return err
	}
	b.mu.Lock()
	b.published[env.ID]++
	b.mu.Unlock()
	return nil
}

func (b *chaosBus) Consume(ctx context.Context, group string, handle func(context.Context, delivery.Message) error) error {
	return b.MemBus.Consume(ctx, group, func(c context.Context, m delivery.Message) error {
		if b.down.Load() {
			return errBusDown
		}
		return handle(c, m)
	})
}

// TestChaosRelayKilledMidBatch (spec 14.7): the relay dies part-way through a
// batch again and again. Each death rolls the batch back, so every outbox row is
// eventually marked relayed exactly once and reaches the bus at least once.
func TestChaosRelayKilledMidBatch(t *testing.T) {
	bus := newChaosBus()
	bus.failEvery = 7 // batches of 5 straddle the failures: some survive, some are killed
	h := newHarnessBus(t, bus)
	const n = 100
	for i := range n {
		h.emit("shipment.dispatched", fmt.Sprintf("shp-%d", i%10))
	}

	crashes := 0
	for h.count(`SELECT count(*) FROM outbox WHERE relayed_at IS NULL`) > 0 {
		if _, err := delivery.RelayOnce(h.ctx, h.db, bus, 5); err != nil {
			crashes++
			if crashes > 1000 {
				t.Fatal("relay never drained the outbox")
			}
		}
	}
	if crashes == 0 {
		t.Fatal("the relay never crashed; the test proved nothing")
	}
	if got := h.count(`SELECT count(*) FROM outbox WHERE relayed_at IS NOT NULL`); got != n {
		t.Errorf("%d rows marked relayed, want %d", got, n)
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	redelivered := 0
	for id := range n {
		c := bus.published[int64(id+1)]
		if c == 0 {
			t.Errorf("outbox row %d never reached the bus", id+1)
		}
		if c > 1 {
			redelivered++
		}
	}
	t.Logf("%d relay crashes, %d rows published more than once (at-least-once)", crashes, redelivered)
}

// TestChaosBusDownDuringDelivery (spec 14.7): the bus goes down while events are
// being relayed and delivered, then comes back. Deliveries pause and resume with
// no loss and no reordering per key.
func TestChaosBusDownDuringDelivery(t *testing.T) {
	bus := newChaosBus()
	h := newHarnessBus(t, bus)
	f := newFake(t)
	h.endpoint(f)
	h.worker(delivery.WorkerConfig{Budget: 50, BreakerThreshold: 1000})

	const keys, perKey = 5, 8
	go func() { // the relay keeps running throughout, as in production
		for h.ctx.Err() == nil {
			_, _ = delivery.RelayOnce(h.ctx, h.db, bus, 10)
			time.Sleep(5 * time.Millisecond)
		}
	}()

	emitted := 0
	for i := range perKey {
		for k := range keys {
			h.emit("shipment.dispatched", fmt.Sprintf("shp-%d", k))
			emitted++
		}
		switch i {
		case 2:
			bus.down.Store(true) // killed
		case 5:
			bus.down.Store(false) // restarted
		}
		time.Sleep(20 * time.Millisecond)
	}

	h.waitFor("every event delivered after the bus came back", func() bool {
		seen := map[int64]bool{}
		for _, r := range f.accepted() {
			seen[r.Seq] = true
		}
		return len(seen) == emitted
	})
	last := map[string]int64{}
	for _, r := range f.accepted() {
		if r.Seq < last[r.Key] {
			t.Fatalf("key %s: event %d delivered after %d", r.Key, r.Seq, last[r.Key])
		}
		last[r.Key] = r.Seq
	}
}
