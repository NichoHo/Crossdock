package delivery_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicholasgasior/crossdock/internal/delivery"
	"github.com/nicholasgasior/crossdock/internal/outbox"
	"github.com/nicholasgasior/crossdock/internal/testdb"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

// ---------------------------------------------------------------------------
// Pure units
// ---------------------------------------------------------------------------

func TestSignVerify(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"id":"evt_1"}`)
	hdr := delivery.SignatureHeader([]string{"new", "old"}, now, body)
	ts := fmt.Sprint(now.Unix())
	for _, secret := range []string{"new", "old"} {
		if !delivery.Verify([]string{secret}, ts, hdr, body, now, 5*time.Minute) {
			t.Errorf("secret %q should verify during an overlap", secret)
		}
	}
	if delivery.Verify([]string{"other"}, ts, hdr, body, now, 5*time.Minute) {
		t.Error("wrong secret verified")
	}
	if delivery.Verify([]string{"new"}, ts, hdr, []byte(`{"id":"evt_2"}`), now, 5*time.Minute) {
		t.Error("tampered body verified")
	}
	if delivery.Verify([]string{"new"}, ts, hdr, body, now.Add(6*time.Minute), 5*time.Minute) {
		t.Error("stale timestamp verified: replay window not enforced")
	}
}

func TestMatches(t *testing.T) {
	cases := []struct {
		filter []string
		topic  string
		want   bool
	}{
		{[]string{"*"}, "stock.moved", true},
		{[]string{"shipment.*"}, "shipment.dispatched", true},
		{[]string{"shipment.*"}, "shipmentx.y", false},
		{[]string{"order.allocated"}, "order.allocated", true},
		{[]string{"order.allocated"}, "order.cancelled", false},
		{[]string{"a", "order.*"}, "order.cancelled", true},
	}
	for _, c := range cases {
		if got := delivery.Matches(c.filter, c.topic); got != c.want {
			t.Errorf("Matches(%v, %q) = %v, want %v", c.filter, c.topic, got, c.want)
		}
	}
}

func TestBackoffBounds(t *testing.T) {
	base, cap := time.Second, time.Hour
	if d := delivery.Backoff(base, cap, 0, func() float64 { return 1 }); d != base {
		t.Errorf("attempt 0 max = %v, want %v", d, base)
	}
	if d := delivery.Backoff(base, cap, 3, func() float64 { return 1 }); d != 8*time.Second {
		t.Errorf("attempt 3 max = %v, want 8s", d)
	}
	if d := delivery.Backoff(base, cap, 40, func() float64 { return 1 }); d != cap {
		t.Errorf("attempt 40 max = %v, want the cap %v", d, cap)
	}
	if d := delivery.Backoff(base, cap, 5, func() float64 { return 0 }); d != 0 {
		t.Errorf("full jitter lower bound = %v, want 0", d)
	}
}

func TestURLPolicy(t *testing.T) {
	for _, bad := range []string{"http://example.com/h", "https://169.254.169.254/latest", "https://user:pw@example.com/", "ftp://x.com/", "https://127.0.0.1/h"} {
		if _, err := delivery.ValidateURL(bad, false); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	if _, err := delivery.ValidateURL("https://hooks.example.com/h", false); err != nil {
		t.Errorf("plain https URL refused: %v", err)
	}
	if _, err := delivery.ValidateURL("http://127.0.0.1:8080/h", true); err != nil {
		t.Errorf("dev loopback refused: %v", err)
	}
	if _, err := delivery.ValidateURL("http://169.254.169.254/", true); err == nil {
		t.Error("metadata address must stay blocked even in dev mode")
	}
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type received struct {
	EventID string
	Seq     int64
	Topic   string
	Key     string // from data.key, set by the tests
	Idem    string
	Replay  string
	Valid   bool
	Status  int
	At      time.Time
	Trace   string
}

// fake is a webhook receiver with programmable behaviour.
type fake struct {
	srv     *httptest.Server
	mu      sync.Mutex
	got     []received
	calls   atomic.Int64
	secrets []string
	// behave returns the status for call number n (1-based). Nil means 200.
	behave func(n int64, r received) int
	delay  time.Duration
}

func newFake(t *testing.T, secrets ...string) *fake {
	f := &fake{secrets: secrets}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Seq  int64  `json:"sequence"`
			Idem string `json:"idempotency_key"`
			Data struct {
				Key string `json:"key"`
			} `json:"data"`
		}
		_ = json.Unmarshal(body, &env)
		f.mu.Lock()
		secrets := append([]string(nil), f.secrets...)
		f.mu.Unlock()
		rec := received{EventID: env.ID, Seq: env.Seq, Topic: env.Type, Key: env.Data.Key, Idem: env.Idem,
			Replay: r.Header.Get("X-Crossdock-Replay-Of"), At: time.Now(), Trace: r.Header.Get("traceparent"),
			Valid: delivery.Verify(secrets, r.Header.Get("X-Crossdock-Timestamp"), r.Header.Get("X-Crossdock-Signature"), body, time.Now(), 5*time.Minute)}
		n := f.calls.Add(1)
		if f.delay > 0 {
			time.Sleep(f.delay)
		}
		rec.Status = 200
		if f.behave != nil {
			rec.Status = f.behave(n, rec)
		}
		f.mu.Lock()
		f.got = append(f.got, rec)
		f.mu.Unlock()
		w.WriteHeader(rec.Status)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) all() []received {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]received(nil), f.got...)
}

func (f *fake) accepted() []received {
	var out []received
	for _, r := range f.all() {
		if r.Status/100 == 2 {
			out = append(out, r)
		}
	}
	return out
}

type harness struct {
	t   *testing.T
	db  *sql.DB
	w   wmstest.World
	svc *delivery.Service
	bus *delivery.MemBus
	ctx context.Context
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db := testdb.Open(t)
	var k [32]byte
	_, _ = rand.Read(k[:])
	enc, err := delivery.NewEncryptor(base64.StdEncoding.EncodeToString(k[:]))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &harness{t: t, db: db, w: wmstest.Seed(t, db, 1), svc: delivery.NewService(db, enc, true), bus: delivery.NewMemBus(), ctx: ctx}
	go func() { _ = delivery.RunFanout(ctx, db, h.bus) }()
	return h
}

func (h *harness) endpoint(f *fake, filter ...string) (delivery.Endpoint, string) {
	h.t.Helper()
	if len(filter) == 0 {
		filter = []string{"*"}
	}
	ep, secret, err := h.svc.Create(h.ctx, string(h.w.WID), f.srv.URL, filter)
	if err != nil {
		h.t.Fatal(err)
	}
	f.mu.Lock()
	f.secrets = []string{secret}
	f.mu.Unlock()
	return ep, secret
}

// emit writes an outbox row and relays it to the bus.
func (h *harness) emit(topic, key string) {
	h.t.Helper()
	if err := outbox.Emit(h.ctx, h.db, string(h.w.WID), topic, key, map[string]string{"key": key}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) relay() {
	h.t.Helper()
	for {
		n, err := delivery.RelayOnce(h.ctx, h.db, h.bus, 100)
		if err != nil {
			h.t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

func (h *harness) worker(cfg delivery.WorkerConfig) {
	if cfg.Workers == 0 {
		cfg.Workers = 4
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = 5 * time.Millisecond
	}
	if cfg.BackoffCap == 0 {
		cfg.BackoffCap = 40 * time.Millisecond
	}
	if cfg.Poll == 0 {
		cfg.Poll = 5 * time.Millisecond
	}
	cfg.AllowPrivate = true
	w := delivery.NewWorker(h.db, h.svc, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go w.Run(h.ctx)
}

func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
}

func (h *harness) count(q string, args ...any) int {
	var n int
	if err := h.db.QueryRow(q, args...).Scan(&n); err != nil {
		h.t.Fatal(err)
	}
	return n
}

// ---------------------------------------------------------------------------
// Behaviour
// ---------------------------------------------------------------------------

func TestDeliversSignedEvent(t *testing.T) {
	h := newHarness(t)
	f := newFake(t)
	ep, _ := h.endpoint(f, "shipment.*")
	h.emit("shipment.dispatched", "shp-1")
	h.emit("stock.moved", "mvt-1") // filtered out
	h.relay()
	h.worker(delivery.WorkerConfig{})

	h.waitFor("delivery", func() bool { return len(f.accepted()) == 1 })
	got := f.accepted()[0]
	if !got.Valid {
		t.Error("signature did not verify")
	}
	if got.Topic != "shipment.dispatched" || got.Idem != got.EventID || got.EventID == "" {
		t.Errorf("envelope = %+v", got)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(f.all()); n != 1 {
		t.Errorf("endpoint called %d times, want 1 (filter must exclude stock.moved)", n)
	}
	st, _ := h.svc.Stats(h.ctx, string(h.w.WID), ep.ID)
	if st.Delivered != 1 || st.Pending != 0 || st.Dead != 0 {
		t.Errorf("stats = %+v", st)
	}
}

func TestFanoutIsIdempotent(t *testing.T) {
	h := newHarness(t)
	f := newFake(t)
	h.endpoint(f)
	h.emit("order.allocated", "ord-1")
	h.relay()
	// The same message arrives again (at-least-once bus).
	var m delivery.Message
	h.waitFor("fanout", func() bool { return h.count(`SELECT count(*) FROM deliveries`) == 1 })
	rows, _ := h.db.Query(`SELECT id, warehouse_id, topic, key, payload, occurred_at FROM events`)
	for rows.Next() {
		var env delivery.Envelope
		var payload []byte
		_ = rows.Scan(&env.ID, &env.WarehouseID, &env.Topic, &env.Key, &payload, &env.OccurredAt)
		env.Payload = payload
		b, _ := json.Marshal(env)
		m = delivery.Message{Key: env.Key, Value: b}
	}
	rows.Close()
	for i := 0; i < 3; i++ {
		if err := delivery.Fanout(h.ctx, h.db, m); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.count(`SELECT count(*) FROM deliveries`); n != 1 {
		t.Fatalf("deliveries = %d, want 1", n)
	}
}

// Events of one shipment arrive in sequence order even when attempts fail and retry.
func TestOrderingPerKeyUnderFailures(t *testing.T) {
	h := newHarness(t)
	f := newFake(t)
	// Every third call fails, so retries interleave with later events.
	f.behave = func(n int64, _ received) int {
		if n%3 == 0 {
			return 500
		}
		return 200
	}
	h.endpoint(f)
	const keys, perKey = 8, 6
	for i := 0; i < perKey; i++ {
		for k := 0; k < keys; k++ {
			h.emit("shipment.dispatched", fmt.Sprintf("shp-%d", k))
		}
	}
	h.relay()
	h.worker(delivery.WorkerConfig{Budget: 50, BreakerThreshold: 1000})

	h.waitFor("all delivered", func() bool { return len(f.accepted()) >= keys*perKey })
	last := map[string]int64{}
	for _, r := range f.accepted() {
		if r.Seq <= last[r.Key] {
			t.Fatalf("key %s: event %d delivered after %d", r.Key, r.Seq, last[r.Key])
		}
		last[r.Key] = r.Seq
	}
	if len(f.accepted()) != keys*perKey {
		t.Errorf("accepted %d deliveries, want exactly %d (no duplicates after success)", len(f.accepted()), keys*perKey)
	}
}

func TestRetriesThenDeadLetterWithEveryAttempt(t *testing.T) {
	h := newHarness(t)
	f := newFake(t)
	f.behave = func(int64, received) int { return 503 }
	ep, _ := h.endpoint(f)
	h.emit("order.allocated", "ord-1")
	h.relay()
	h.worker(delivery.WorkerConfig{Budget: 4, BreakerThreshold: 1000})

	h.waitFor("dead letter", func() bool {
		st, _ := h.svc.Stats(h.ctx, string(h.w.WID), ep.ID)
		return st.Dead == 1
	})
	if n := len(f.all()); n != 4 {
		t.Errorf("attempts = %d, want the budget of 4", n)
	}
	var id string
	_ = h.db.QueryRow(`SELECT id FROM deliveries`).Scan(&id)
	atts, err := h.svc.Attempts(h.ctx, string(h.w.WID), id)
	if err != nil || len(atts) != 4 {
		t.Fatalf("recorded attempts = %d, %v", len(atts), err)
	}
	for _, a := range atts {
		if a.Status != 503 || a.Error == "" {
			t.Errorf("attempt %d = %+v, want status 503 and an error text", a.N, a)
		}
	}
}

func TestCircuitBreakerOpensProbesAndCloses(t *testing.T) {
	h := newHarness(t)
	f := newFake(t)
	var healthy atomic.Bool
	f.behave = func(int64, received) int {
		if healthy.Load() {
			return 200
		}
		return 500
	}
	ep, _ := h.endpoint(f)
	for i := 0; i < 12; i++ {
		h.emit("stock.moved", fmt.Sprintf("mvt-%d", i))
	}
	h.relay()
	h.worker(delivery.WorkerConfig{Budget: 100, BreakerThreshold: 3, BreakerCooldown: 400 * time.Millisecond})

	h.waitFor("breaker to open", func() bool { e, _ := h.svc.Get(h.ctx, string(h.w.WID), ep.ID); return e.Breaker == "open" })
	atOpen := f.calls.Load()
	if atOpen != 3 {
		t.Errorf("calls when the breaker opened = %d, want exactly the threshold 3", atOpen)
	}
	time.Sleep(200 * time.Millisecond) // still cooling down
	if n := f.calls.Load(); n != atOpen {
		t.Errorf("endpoint was called %d more times while the breaker was open", n-atOpen)
	}
	// After the cooldown exactly one probe goes out, fails, and re-opens it.
	h.waitFor("probe", func() bool { return f.calls.Load() == atOpen+1 })
	time.Sleep(100 * time.Millisecond)
	if n := f.calls.Load(); n != atOpen+1 {
		t.Errorf("half-open let %d requests through, want 1", n-atOpen)
	}
	healthy.Store(true)
	h.waitFor("recovery", func() bool { return len(f.accepted()) == 12 })
	e, _ := h.svc.Get(h.ctx, string(h.w.WID), ep.ID)
	if e.Breaker != "closed" || e.ConsecutiveFailures != 0 {
		t.Errorf("breaker after recovery = %s/%d, want closed/0", e.Breaker, e.ConsecutiveFailures)
	}
}

// One failing customer never starves the others (spec 7.2).
func TestFailingEndpointDoesNotSlowHealthyOnes(t *testing.T) {
	h := newHarness(t)
	bad := newFake(t)
	bad.delay = 150 * time.Millisecond
	bad.behave = func(int64, received) int { return 500 }
	good := newFake(t)
	h.endpoint(bad)
	h.endpoint(good)
	const n = 30
	for i := 0; i < n; i++ {
		h.emit("stock.moved", fmt.Sprintf("mvt-%d", i))
	}
	h.relay()
	start := time.Now()
	h.worker(delivery.WorkerConfig{Workers: 4, Budget: 100, BreakerThreshold: 1000})
	h.waitFor("healthy endpoint to finish", func() bool { return len(good.accepted()) == n })
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("healthy endpoint took %v while another endpoint was failing", d)
	}
}

func TestReplayDeadLettersIsIdempotentForReceivers(t *testing.T) {
	h := newHarness(t)
	f := newFake(t)
	var broken atomic.Bool
	broken.Store(true)
	f.behave = func(int64, received) int {
		if broken.Load() {
			return 500
		}
		return 200
	}
	ep, _ := h.endpoint(f)
	h.emit("order.allocated", "ord-1")
	h.emit("order.allocated", "ord-2")
	h.relay()
	h.worker(delivery.WorkerConfig{Budget: 2, BreakerThreshold: 1000})
	h.waitFor("two dead letters", func() bool {
		st, _ := h.svc.Stats(h.ctx, string(h.w.WID), ep.ID)
		return st.Dead == 2
	})

	broken.Store(false)
	n, err := h.svc.ReplayDead(h.ctx, string(h.w.WID), ep.ID)
	if err != nil || n != 2 {
		t.Fatalf("ReplayDead = %d, %v", n, err)
	}
	h.waitFor("replayed deliveries", func() bool { return len(f.accepted()) == 2 })
	for _, r := range f.accepted() {
		if r.Replay != r.EventID || r.Idem != r.EventID {
			t.Errorf("replay headers = %+v: want X-Crossdock-Replay-Of and the original idempotency key", r)
		}
	}
	st, _ := h.svc.Stats(h.ctx, string(h.w.WID), ep.ID)
	if st.Dead != 0 {
		t.Errorf("dead depth after replay = %d, want 0", st.Dead)
	}
	if n, _ := h.svc.ReplayDead(h.ctx, string(h.w.WID), ep.ID); n != 0 {
		t.Errorf("second ReplayDead queued %d, want 0", n)
	}
}

func TestReplaySingleEventAndRange(t *testing.T) {
	h := newHarness(t)
	f := newFake(t)
	ep, _ := h.endpoint(f)
	for i := 0; i < 3; i++ {
		h.emit("stock.moved", fmt.Sprintf("m%d", i))
	}
	h.relay()
	h.worker(delivery.WorkerConfig{})
	h.waitFor("first pass", func() bool { return len(f.accepted()) == 3 })

	var first int64
	_ = h.db.QueryRow(`SELECT min(id) FROM events`).Scan(&first)
	if err := h.svc.ReplayEvent(h.ctx, string(h.w.WID), ep.ID, first); err != nil {
		t.Fatal(err)
	}
	h.waitFor("single replay", func() bool { return len(f.accepted()) == 4 })

	n, err := h.svc.ReplayRange(h.ctx, string(h.w.WID), ep.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil || n != 3 {
		t.Fatalf("ReplayRange = %d, %v", n, err)
	}
	h.waitFor("range replay", func() bool { return len(f.accepted()) == 7 })
	if err := h.svc.ReplayEvent(h.ctx, string(h.w.WID), ep.ID, 999999); err == nil {
		t.Error("replaying an unknown event should fail")
	}
}

func TestSecretRotationOverlap(t *testing.T) {
	h := newHarness(t)
	f := newFake(t)
	ep, oldSecret := h.endpoint(f)
	newSecret, err := h.svc.Rotate(h.ctx, string(h.w.WID), ep.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.secrets = []string{newSecret} // the receiver already switched to the new secret
	f.mu.Unlock()
	h.emit("stock.moved", "m1")
	h.relay()
	h.worker(delivery.WorkerConfig{})
	h.waitFor("delivery", func() bool { return len(f.accepted()) == 1 })
	if !f.accepted()[0].Valid {
		t.Error("event signed during the overlap must verify with the new secret")
	}
	// A receiver still on the old secret also verifies the same request.
	f.mu.Lock()
	f.secrets = []string{oldSecret}
	f.mu.Unlock()
	h.emit("stock.moved", "m2")
	h.relay()
	h.waitFor("second delivery", func() bool { return len(f.accepted()) == 2 })
	if !f.accepted()[1].Valid {
		t.Error("the old secret must keep verifying during the overlap window")
	}
}

func TestEndpointValidationRejectsUnsafeTargets(t *testing.T) {
	h := newHarness(t)
	strict := delivery.NewService(h.db, nil, false)
	if _, _, err := strict.Create(h.ctx, string(h.w.WID), "http://169.254.169.254/latest", []string{"*"}); err == nil {
		t.Error("cloud metadata URL accepted")
	}
	if _, _, err := h.svc.Create(h.ctx, string(h.w.WID), "https://hooks.example.com/h", nil); err == nil {
		t.Error("endpoint with no event filter accepted")
	}
}

// A secret that cannot be decrypted fails the attempt like any other error. It
// must not make a worker spin on the same delivery.
func TestUndecryptableSecretFailsTheAttempt(t *testing.T) {
	h := newHarness(t)
	f := newFake(t)
	ep, _ := h.endpoint(f)
	if _, err := h.db.Exec(`UPDATE endpoint_secrets SET secret_enc = '\x00112233445566778899aabbccddeeff'`); err != nil {
		t.Fatal(err)
	}
	h.emit("order.allocated", "ord-1")
	h.relay()
	h.worker(delivery.WorkerConfig{Budget: 3, BreakerThreshold: 1000})
	h.waitFor("dead letter", func() bool {
		st, _ := h.svc.Stats(h.ctx, string(h.w.WID), ep.ID)
		return st.Dead == 1
	})
	if n := len(f.all()); n != 0 {
		t.Errorf("endpoint was called %d times without a usable signature", n)
	}
	var msg string
	_ = h.db.QueryRow(`SELECT last_error FROM deliveries`).Scan(&msg)
	if !strings.Contains(msg, "signing secret unavailable") {
		t.Errorf("last_error = %q", msg)
	}
}
