package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nicholasgasior/crossdock/internal/api"
	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/testdb"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

func limitedEnv(t *testing.T, l api.Limits) *env {
	t.Helper()
	db := testdb.Open(t)
	a, err := auth.New(db, serviceToken, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := os.ReadFile("../../api/openapi.yaml")
	srv := api.New(db, a, slog.New(slog.NewTextHandler(io.Discard, nil)), spec, api.WithLimits(l))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &env{t: t, db: db, store: inventory.NewStore(db), ts: ts, w: wmstest.Seed(t, db, 1)}
}

// A caller who sends too fast is told to wait; another caller is not affected.
func TestRateLimitIsPerPrincipalAndSaysWhenToRetry(t *testing.T) {
	e := limitedEnv(t, api.Limits{User: api.RateLimit{PerSecond: 1, Burst: 3}, System: api.RateLimit{PerSecond: 1, Burst: 1}})
	fast := user("olga", auth.RoleOperator)
	var limited int
	for i := 0; i < 6; i++ {
		st, _ := e.do(fast, "GET", e.wpath("/catalogue"), nil)
		if st == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited < 2 {
		t.Errorf("only %d of 6 immediate requests were limited, want at least 2 after a burst of 3", limited)
	}
	req, _ := http.NewRequest("GET", e.ts.URL+e.wpath("/catalogue"), nil)
	fast(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Errorf("status %d, Retry-After %q; want 429 with a Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if st, b := e.do(user("ada", auth.RoleOperator), "GET", e.wpath("/catalogue"), nil); st != 200 {
		t.Errorf("a different user was limited too: %d %s", st, b)
	}
}

// Answers depend on who asks, and an error carries a trace id so support can find the log line.
func TestResponsesAreNotCachedAndErrorsCarryATraceID(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequest("GET", e.ts.URL+e.wpath("/tasks/nope/count-sheet"), nil)
	user("sue", auth.RoleSupervisor)(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", resp.Header.Get("Cache-Control"))
	}
	var p struct {
		TraceID string `json:"trace_id"`
		Detail  string
	}
	if err := json.Unmarshal(b, &p); err != nil || len(p.TraceID) != 32 {
		t.Errorf("error body %s lacks a 32 character trace_id", b)
	}
	if strings.Contains(strings.ToLower(p.Detail), "sql") || strings.Contains(p.Detail, "pq:") {
		t.Errorf("error detail leaks internals: %q", p.Detail)
	}
}


// Mass assignment: a field the endpoint does not define is refused, not ignored.
func TestUnknownFieldsAreRefused(t *testing.T) {
	e := newEnv(t)
	st, b := e.do(user("sue", auth.RoleSupervisor), "POST", e.wpath("/orders"), map[string]any{
		"ref": "SO-1", "lines": []map[string]any{{"sku": string(e.w.SKUs[0]), "qty": 1}}, "warehouse_id": "wh-other", "role": "admin"})
	expect(t, st, b, 400)
}

// An operator lists their own tasks and nobody else's, whatever filter they ask for.
func TestOperatorSeesOnlyTheirOwnTasks(t *testing.T) {
	e := newEnv(t)
	for _, who := range []string{"olga", "ada"} {
		if _, err := e.db.Exec(`INSERT INTO tasks (id, warehouse_id, kind, state, assigned_to, sku_id, qty, ref, created_at, updated_at)
			VALUES ($1,$2,'pick','assigned',$3,$4,1,$1,NOW(),NOW())`, "tsk-"+who, string(e.w.WID), who, string(e.w.SKUs[0])); err != nil {
			t.Fatal(err)
		}
	}
	st, b := e.do(user("olga", auth.RoleOperator), "GET", e.wpath("/tasks?assigned_to=ada"), nil)
	expect(t, st, b, 200)
	if !contains(b, "tsk-olga") || contains(b, "tsk-ada") {
		t.Errorf("operator olga saw %s", b)
	}
}
