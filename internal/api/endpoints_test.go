package api_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nicholasgasior/crossdock/internal/api"
	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/delivery"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/testdb"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// newWebhookEnv is newEnv with webhook delivery enabled and an in-memory bus.
func newWebhookEnv(t *testing.T) (*env, *delivery.MemBus, *delivery.Service) {
	t.Helper()
	db := testdb.Open(t)
	a, err := auth.New(db, serviceToken, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	var k [32]byte
	_, _ = rand.Read(k[:])
	enc, _ := delivery.NewEncryptor(base64.StdEncoding.EncodeToString(k[:]))
	svc := delivery.NewService(db, enc, true)
	srv := api.New(db, a, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, api.WithDelivery(svc))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &env{t: t, db: db, store: inventory.NewStore(db), ts: ts, w: wmstest.Seed(t, db, 2)}, delivery.NewMemBus(), svc
}

func TestEndpointManagementRolesAndSecrets(t *testing.T) {
	e, _, _ := newWebhookEnv(t)
	admin, sup := user("ada", auth.RoleAdmin), user("sue", auth.RoleSupervisor)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer hook.Close()

	// Only an admin creates endpoints.
	st, b := e.do(sup, "POST", e.wpath("/endpoints"), map[string]any{"url": hook.URL, "events": []string{"*"}})
	expect(t, st, b, 403)
	st, b = e.do(admin, "POST", e.wpath("/endpoints"), map[string]any{"url": hook.URL, "events": []string{"shipment.*"}}, "Idempotency-Key", "k1")
	expect(t, st, b, 201)
	var created struct {
		Endpoint struct{ ID string } `json:"endpoint"`
		Secret   string              `json:"secret"`
	}
	mustJSON(t, b, &created)
	if len(created.Secret) < 20 {
		t.Fatalf("no secret returned: %s", b)
	}
	// The one-time secret must not be kept for idempotent replay.
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM idempotency_keys`).Scan(&n)
	if n != 0 {
		t.Errorf("idempotency_keys holds %d rows after creating an endpoint, want 0 (it would store the secret)", n)
	}
	// Reads never return the secret; the database never holds it in plaintext.
	st, b = e.do(sup, "GET", e.wpath("/endpoints/"+created.Endpoint.ID), nil)
	expect(t, st, b, 200)
	if contains(b, created.Secret) {
		t.Error("GET endpoint leaked the secret")
	}
	var plainHits int
	_ = e.db.QueryRow(`SELECT count(*) FROM endpoint_secrets WHERE position($1::bytea in secret_enc) > 0`, created.Secret).Scan(&plainHits)
	if plainHits != 0 {
		t.Error("secret stored in plaintext")
	}
	var hash string
	_ = e.db.QueryRow(`SELECT secret_hash FROM endpoint_secrets`).Scan(&hash)
	if hash != delivery.HashSecret(created.Secret) {
		t.Error("stored hash does not match the secret")
	}

	// Unsafe targets and bad filters are refused.
	st, b = e.do(admin, "POST", e.wpath("/endpoints"), map[string]any{"url": "http://169.254.169.254/x", "events": []string{"*"}})
	expect(t, st, b, 422)
	st, b = e.do(admin, "POST", e.wpath("/endpoints"), map[string]any{"url": hook.URL, "events": []string{}})
	expect(t, st, b, 422)

	// Rotation returns a new secret, also once.
	st, b = e.do(admin, "POST", e.wpath("/endpoints/"+created.Endpoint.ID+"/rotate-secret"), nil)
	expect(t, st, b, 200)
	var rot struct{ Secret string }
	mustJSON(t, b, &rot)
	if rot.Secret == "" || rot.Secret == created.Secret {
		t.Errorf("rotate = %s", b)
	}
	st, b = e.do(admin, "DELETE", e.wpath("/endpoints/"+created.Endpoint.ID), nil)
	expect(t, st, b, 204)
	st, b = e.do(sup, "GET", e.wpath("/endpoints/"+created.Endpoint.ID), nil)
	expect(t, st, b, 200)
}

func contains(b []byte, s string) bool { return len(s) > 0 && indexOf(string(b), s) >= 0 }

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

// TestTraceFollowsScannerConfirmToWebhook is the Phase 2 exit check: one trace
// id runs from the scanner's confirm request, through the API, the outbox
// relay, the bus and the fan-out, to the signed webhook call.
func TestTraceFollowsScannerConfirmToWebhook(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sdktrace.AlwaysSample())))
	otel.SetTextMapPropagator(propagation.TraceContext{})

	e, bus, svc := newWebhookEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = delivery.RunFanout(ctx, e.db, bus) }()

	var mu sync.Mutex
	var gotTrace string
	var gotValid bool
	secretCh := make(chan string, 1)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		secret := <-secretCh
		secretCh <- secret
		mu.Lock()
		gotTrace = r.Header.Get("traceparent")
		gotValid = delivery.Verify([]string{secret}, r.Header.Get("X-Crossdock-Timestamp"), r.Header.Get("X-Crossdock-Signature"), body, time.Now(), 5*time.Minute)
		mu.Unlock()
	}))
	defer hook.Close()

	ep, secret, err := svc.Create(ctx, string(e.w.WID), hook.URL, []string{"stock.moved"})
	if err != nil {
		t.Fatal(err)
	}
	_ = ep
	secretCh <- secret
	go delivery.NewWorker(e.db, svc, delivery.WorkerConfig{Workers: 2, Poll: 5 * time.Millisecond, AllowPrivate: true},
		slog.New(slog.NewTextHandler(io.Discard, nil))).Run(ctx)

	// An ASN, then the scanner's receive confirm carrying its own trace.
	sup, op := user("sue", auth.RoleSupervisor), user("olga", auth.RoleOperator)
	st, b := e.do(sup, "POST", e.wpath("/asns"), map[string]any{"ref": "PO-T", "lines": []map[string]any{{"sku": e.w.SKUs[0], "expected_qty": 3}}})
	expect(t, st, b, 201)
	var asn struct{ Lines []struct{ ID string } }
	mustJSON(t, b, &asn)

	traceID := hex.EncodeToString(randBytes(16))
	scannerSpan := hex.EncodeToString(randBytes(8))
	st, b = e.do(op, "POST", e.wpath("/commands"), map[string]any{"commands": []map[string]any{
		{"key": "scan-1", "type": "receive", "payload": map[string]any{"asn_line_id": asn.Lines[0].ID, "qty": 3, "location": e.w.Dock}},
	}}, "traceparent", "00-"+traceID+"-"+scannerSpan+"-01")
	expect(t, st, b, 200)

	// Outbox -> bus (the relay), then fan-out and the worker run on their own.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := delivery.RelayOnce(ctx, e.db, bus, 50); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		done := gotTrace != ""
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotTrace == "" {
		t.Fatal("webhook was never delivered")
	}
	if !gotValid {
		t.Error("webhook signature did not verify")
	}
	if len(gotTrace) < 55 || gotTrace[3:35] != traceID {
		t.Fatalf("webhook traceparent = %q, want trace id %s from the scanner request", gotTrace, traceID)
	}
	// The delivery span ends after the HTTP call returns and the result is stored.
	seen := map[string]bool{}
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		seen = map[string]bool{}
		for _, s := range rec.Ended() {
			if s.SpanContext().TraceID().String() == traceID {
				seen[s.Name()] = true
			}
		}
		if seen["webhook.deliver"] {
			break
		}
	}
	for _, want := range []string{"POST /v1/warehouses/{wid}/commands", "outbox.relay", "delivery.fanout", "webhook.deliver"} {
		if !seen[want] {
			t.Errorf("no %q span in the scanner's trace; spans in that trace: %v", want, seen)
		}
	}
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}
