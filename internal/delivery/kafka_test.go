package delivery_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nicholasgasior/crossdock/internal/delivery"
)

// TestKafkaBusRoundTrip needs a broker: CROSSDOCK_TEST_BROKERS=127.0.0.1:19092
// (docker compose -f deploy/docker-compose.dev.yml up -d redpanda).
// It checks per-key order, and that a failed handler gets the message again.
func TestKafkaBusRoundTrip(t *testing.T) {
	brokers := os.Getenv("CROSSDOCK_TEST_BROKERS")
	if brokers == "" {
		t.Skip("CROSSDOCK_TEST_BROKERS not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	topic := fmt.Sprintf("crossdock.test.%d", time.Now().UnixNano())
	bus, err := delivery.NewKafkaBus(ctx, []string{brokers}, topic, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	const perKey = 20
	for i := 0; i < perKey; i++ {
		for _, k := range []string{"shp-a", "shp-b", "shp-c"} {
			if err := bus.Publish(ctx, delivery.Message{Key: k, Value: []byte(fmt.Sprintf("%s:%03d", k, i)), Headers: map[string]string{"traceparent": "x"}}); err != nil {
				t.Fatal(err)
			}
		}
	}

	var mu sync.Mutex
	got := map[string][]string{}
	failedOnce := false
	done := make(chan struct{})
	total := 0
	go func() {
		_ = bus.Consume(ctx, "test-group-"+topic, func(_ context.Context, m delivery.Message) error {
			mu.Lock()
			defer mu.Unlock()
			if !failedOnce { // the first message must come back
				failedOnce = true
				return fmt.Errorf("transient failure")
			}
			if m.Headers["traceparent"] != "x" {
				t.Errorf("header lost: %v", m.Headers)
			}
			got[m.Key] = append(got[m.Key], string(m.Value))
			total++
			if total == perKey*3 {
				close(done)
			}
			return nil
		})
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("timed out; received %d of %d", total, perKey*3)
	}
	for k, vs := range got {
		if len(vs) != perKey {
			t.Errorf("%s: got %d messages, want %d (a retried message must not be lost or duplicated after success)", k, len(vs), perKey)
		}
		for i, v := range vs {
			if want := fmt.Sprintf("%s:%03d", k, i); v != want {
				t.Errorf("%s position %d = %s, want %s: order broken", k, i, v, want)
				break
			}
		}
	}
}
