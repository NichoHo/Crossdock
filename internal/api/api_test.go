package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicholasgasior/crossdock/internal/api"
	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/testdb"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

const (
	serviceToken = "test-service-token-0123456789abcdef0123456789"
	signingKey   = "test-signing-key-0123456789abcdef0123456789ab"
)

type env struct {
	t     *testing.T
	db    *sql.DB
	store *inventory.Store
	ts    *httptest.Server
	w     wmstest.World
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := testdb.Open(t)
	a, err := auth.New(db, serviceToken, signingKey)
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := os.ReadFile("../../api/openapi.yaml")
	srv := api.New(db, a, slog.New(slog.NewTextHandler(io.Discard, nil)), spec, api.WithLimits(api.Limits{})) // limits are tested on their own
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &env{t: t, db: db, store: inventory.NewStore(db), ts: ts, w: wmstest.Seed(t, db, 3)}
}

// caller authenticates a request.
type caller func(r *http.Request)

func user(name string, role auth.Role) caller {
	return func(r *http.Request) {
		id, sig := auth.SignIdentity(signingKey, name, role, time.Now())
		r.Header.Set("Authorization", "Bearer "+serviceToken)
		r.Header.Set("X-Crossdock-Identity", id)
		r.Header.Set("X-Crossdock-Signature", sig)
	}
}

func token(tok string) caller { return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) } }

func (e *env) do(who caller, method, path string, body any, hdr ...string) (int, []byte) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if who != nil {
		who(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (e *env) wpath(suffix string) string { return "/v1/warehouses/" + string(e.w.WID) + suffix }

func mustJSON(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("bad json %s: %v", b, err)
	}
}

func expect(t *testing.T, got int, body []byte, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("status = %d, want %d; body: %s", got, want, body)
	}
}

// TestOpenAPIMatchesRoutes: the contract and the router describe the same endpoints.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pathRe := regexp.MustCompile(`^  (/\S+):\s*$`)
	methRe := regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	var cur string
	have := map[string]bool{}
	for _, line := range strings.Split(string(spec), "\n") {
		line = strings.TrimRight(line, "\r")
		if m := pathRe.FindStringSubmatch(line); m != nil {
			cur = m[1]
		} else if m := methRe.FindStringSubmatch(line); m != nil && cur != "" {
			have[strings.ToUpper(m[1])+" "+cur] = true
		}
	}
	want := map[string]bool{}
	for _, r := range api.Routes() {
		want[r.Method+" "+r.Pattern] = true
	}
	var missing, extra []string
	for k := range want {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	for k := range have {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing)+len(extra) > 0 {
		t.Fatalf("routes missing from openapi.yaml: %v\nin openapi.yaml but not routed: %v", missing, extra)
	}
}

// TestRoleMatrix: for every protected endpoint, each role and token kind is
// either refused (401/403) or let through, exactly as the route declares.
func TestRoleMatrix(t *testing.T) {
	e := newEnv(t)
	_, plainNoScope, _ := auth.CreateToken(context.Background(), e.db, "noscope", []string{"events:read"})
	roles := []auth.Role{auth.RoleOperator, auth.RoleSupervisor, auth.RoleAdmin}

	for _, rt := range api.Routes() {
		path := strings.NewReplacer("{wid}", string(e.w.WID), "{id}", "missing-id").Replace(rt.Pattern)
		name := rt.Method + " " + rt.Pattern

		if st, b := e.do(nil, rt.Method, path, nil); st != 401 {
			t.Errorf("%s: unauthenticated = %d, want 401 (%s)", name, st, b)
		}
		for _, role := range roles {
			st, b := e.do(user("u-"+string(role), role), rt.Method, path, map[string]any{})
			should := role.AtLeast(rt.MinRole)
			if should && (st == 401 || st == 403) {
				t.Errorf("%s: role %s refused with %d (%s)", name, role, st, b)
			}
			if !should && st != 403 {
				t.Errorf("%s: role %s = %d, want 403", name, role, st)
			}
		}
		// A system token without the route's scope is always refused.
		if st, _ := e.do(token(plainNoScope), rt.Method, path, map[string]any{}); st != 403 {
			t.Errorf("%s: wrong-scope token = %d, want 403", name, st)
		}
		if rt.Scope != "" {
			_, tok, _ := auth.CreateToken(context.Background(), e.db, "s-"+rt.Scope, []string{rt.Scope})
			if st, b := e.do(token(tok), rt.Method, path, map[string]any{}); st == 401 || st == 403 {
				t.Errorf("%s: token with scope %s refused with %d (%s)", name, rt.Scope, st, b)
			}
		}
	}
}

func TestAuthRejectsTampering(t *testing.T) {
	e := newEnv(t)
	path := "/v1/me"
	// Wrong service token.
	if st, _ := e.do(token("x"+serviceToken), "GET", path, nil); st != 401 {
		t.Errorf("bad service token = %d", st)
	}
	// Forged role: valid signature for operator, payload edited to admin.
	id, sig := auth.SignIdentity(signingKey, "mallory", auth.RoleOperator, time.Now())
	forged, _ := auth.SignIdentity(signingKey, "mallory", auth.RoleAdmin, time.Now())
	for _, c := range []struct{ name, id, sig string }{
		{"forged identity", forged, sig},
		{"wrong key", id, strings.Repeat("0", 64)},
	} {
		st, _ := e.do(func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+serviceToken)
			r.Header.Set("X-Crossdock-Identity", c.id)
			r.Header.Set("X-Crossdock-Signature", c.sig)
		}, "GET", path, nil)
		if st != 401 {
			t.Errorf("%s = %d, want 401", c.name, st)
		}
	}
	// Stale timestamp.
	oldID, oldSig := auth.SignIdentity(signingKey, "u", auth.RoleAdmin, time.Now().Add(-time.Hour))
	if st, _ := e.do(func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+serviceToken)
		r.Header.Set("X-Crossdock-Identity", oldID)
		r.Header.Set("X-Crossdock-Signature", oldSig)
	}, "GET", path, nil); st != 401 {
		t.Errorf("stale identity = %d, want 401", st)
	}
	// Service token without identity.
	if st, _ := e.do(token(serviceToken), "GET", path, nil); st != 401 {
		t.Errorf("service token alone = %d, want 401", st)
	}
}

// TestHTTPFlow runs the whole cycle over HTTP with the three kinds of caller.
func TestHTTPFlow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sup, op := user("sue", auth.RoleSupervisor), user("olga", auth.RoleOperator)
	_, asnTok, _ := auth.CreateToken(ctx, e.db, "erp-asn", []string{"asn:write"})
	_, ordTok, _ := auth.CreateToken(ctx, e.db, "shop", []string{"orders:write"})

	// ASN from an external system.
	st, b := e.do(token(asnTok), "POST", e.wpath("/asns"), map[string]any{
		"ref": "PO-9", "supplier": "Acme", "lines": []map[string]any{{"sku": e.w.SKUs[0], "expected_qty": 8}},
	})
	expect(t, st, b, 201)
	var asn struct {
		ID    string `json:"id"`
		Lines []struct {
			ID string `json:"id"`
		} `json:"lines"`
	}
	mustJSON(t, b, &asn)

	// Receive requires Idempotency-Key.
	st, b = e.do(op, "POST", e.wpath("/receipts"), map[string]any{"asn_line_id": asn.Lines[0].ID, "qty": 8, "location": e.w.Dock})
	expect(t, st, b, 422)
	st, b = e.do(op, "POST", e.wpath("/receipts"),
		map[string]any{"asn_line_id": asn.Lines[0].ID, "qty": 8, "lpn": "LPN-A", "location": e.w.Dock}, "Idempotency-Key", "rcv-1")
	expect(t, st, b, 201)
	var rcv struct {
		PutawayTaskID string `json:"putaway_task_id"`
	}
	mustJSON(t, b, &rcv)

	// Same key + same body replays; same key + different body is refused.
	st, b = e.do(op, "POST", e.wpath("/receipts"),
		map[string]any{"asn_line_id": asn.Lines[0].ID, "qty": 8, "lpn": "LPN-A", "location": e.w.Dock}, "Idempotency-Key", "rcv-1")
	expect(t, st, b, 201)
	st, b = e.do(op, "POST", e.wpath("/receipts"),
		map[string]any{"asn_line_id": asn.Lines[0].ID, "qty": 9, "lpn": "LPN-A", "location": e.w.Dock}, "Idempotency-Key", "rcv-1")
	expect(t, st, b, 422)

	// An operator cannot complete a task nobody gave them.
	st, b = e.do(op, "POST", e.wpath("/tasks/"+rcv.PutawayTaskID+"/complete"), nil)
	expect(t, st, b, 403)
	st, b = e.do(sup, "POST", e.wpath("/tasks/"+rcv.PutawayTaskID+"/assign"), map[string]any{"operator": "olga"})
	expect(t, st, b, 204)
	st, b = e.do(op, "POST", e.wpath("/tasks/"+rcv.PutawayTaskID+"/complete"), nil)
	expect(t, st, b, 200)

	// Order from an external system, then supervisor drives allocation and the wave.
	st, b = e.do(token(ordTok), "POST", e.wpath("/orders"), map[string]any{
		"ref": "SO-9", "lines": []map[string]any{{"sku": e.w.SKUs[0], "qty": 3}},
	})
	expect(t, st, b, 201)
	var ord struct {
		ID string `json:"id"`
	}
	mustJSON(t, b, &ord)
	st, b = e.do(sup, "POST", e.wpath("/allocation-sweeps"), nil)
	expect(t, st, b, 200)
	st, b = e.do(sup, "POST", e.wpath("/waves"), map[string]any{"order_ids": []string{ord.ID}})
	expect(t, st, b, 201)

	// Operator claims and completes pick then pack.
	for _, kind := range []string{"pick", "pack"} {
		st, b = e.do(op, "POST", e.wpath("/tasks/claim"), map[string]any{"kinds": []string{kind}})
		expect(t, st, b, 200)
		var tk struct {
			ID string `json:"id"`
		}
		mustJSON(t, b, &tk)
		st, b = e.do(op, "POST", e.wpath("/tasks/"+tk.ID+"/start"), nil)
		expect(t, st, b, 200)
		st, b = e.do(op, "POST", e.wpath("/tasks/"+tk.ID+"/complete"), nil)
		expect(t, st, b, 200)
	}
	st, b = e.do(op, "POST", e.wpath("/tasks/claim"), nil)
	expect(t, st, b, 204)

	// Ship through the offline-sync command path, as the scanner does.
	st, b = e.do(op, "POST", e.wpath("/commands"), map[string]any{"commands": []map[string]any{
		{"key": "ship-1", "type": "ship", "payload": map[string]any{"order_id": ord.ID, "carrier": "JNT", "tracking": "T1", "cod_amount_minor": 99900}},
	}})
	expect(t, st, b, 200)
	var shipRes struct{ Results []struct{ Status string } }
	mustJSON(t, b, &shipRes)
	if len(shipRes.Results) != 1 || shipRes.Results[0].Status != "applied" {
		t.Fatalf("ship command = %s", b)
	}
	var cod int64
	if err := e.db.QueryRow(`SELECT cod_amount_minor FROM shipments WHERE warehouse_id=$1 AND order_id=$2`, string(e.w.WID), ord.ID).Scan(&cod); err != nil || cod != 99900 {
		t.Fatalf("shipment cod = %d, %v", cod, err)
	}

	// Reads: stock explorer and timeline.
	st, b = e.do(op, "GET", e.wpath("/stock?sku="+string(e.w.SKUs[0])), nil)
	expect(t, st, b, 200)
	var stock struct {
		Items []struct{ Available int64 }
	}
	mustJSON(t, b, &stock)
	if len(stock.Items) != 1 || stock.Items[0].Available != 5 {
		t.Errorf("stock = %s, want one position with 5 available", b)
	}
	st, b = e.do(op, "GET", e.wpath("/movements?order_id="+ord.ID+"&limit=2"), nil)
	expect(t, st, b, 200)
	var mv struct {
		Items      []map[string]any
		NextCursor string `json:"next_cursor"`
	}
	mustJSON(t, b, &mv)
	if len(mv.Items) != 2 || mv.NextCursor == "" {
		t.Errorf("timeline page = %s, want 2 items and a cursor", b)
	}
	wmstest.CheckInvariants(t, e.db, e.store, e.w.WID)
}

// TestCommandsIdempotentAcrossScanners: two scanners sync the same offline
// command concurrently and a retry later; there is one movement (spec 14.2).
func TestCommandsIdempotentAcrossScanners(t *testing.T) {
	e := newEnv(t)
	sup := user("sue", auth.RoleSupervisor)
	op := user("olga", auth.RoleOperator)
	st, b := e.do(sup, "POST", e.wpath("/asns"), map[string]any{
		"ref": "PO-C", "lines": []map[string]any{{"sku": e.w.SKUs[0], "expected_qty": 10}},
	})
	expect(t, st, b, 201)
	var asn struct{ Lines []struct{ ID string } }
	mustJSON(t, b, &asn)

	batch := map[string]any{"commands": []map[string]any{
		{"key": "scan-1", "type": "receive", "payload": map[string]any{"asn_line_id": asn.Lines[0].ID, "qty": 4, "location": e.w.Dock}},
		{"key": "bad-1", "type": "pick", "payload": map[string]any{"task_id": "nope"}},
	}}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if st, b := e.do(op, "POST", e.wpath("/commands"), batch); st != 200 {
				t.Errorf("commands = %d %s", st, b)
			}
		}()
	}
	wg.Wait()
	st, b = e.do(op, "POST", e.wpath("/commands"), batch)
	expect(t, st, b, 200)
	var res struct{ Results []struct{ Key, Status string; Code int } }
	mustJSON(t, b, &res)
	if res.Results[0].Status != "duplicate" || res.Results[1].Status != "duplicate" || res.Results[1].Code != 404 {
		t.Errorf("replay results = %s", b)
	}
	var n int
	_ = e.db.QueryRow(`SELECT count(*) FROM movements WHERE warehouse_id=$1 AND kind='receive'`, string(e.w.WID)).Scan(&n)
	if n != 1 {
		t.Fatalf("receive movements = %d, want 1", n)
	}
	wmstest.CheckInvariants(t, e.db, e.store, e.w.WID)
}

func TestProblemDetailsAndPagination(t *testing.T) {
	e := newEnv(t)
	sup := user("sue", auth.RoleSupervisor)
	st, b := e.do(sup, "GET", e.wpath("/orders/nope"), nil)
	expect(t, st, b, 404)
	var p struct {
		Title  string
		Status int
	}
	mustJSON(t, b, &p)
	if p.Status != 404 || p.Title == "" {
		t.Errorf("problem body = %s", b)
	}
	for i := 0; i < 5; i++ {
		st, b := e.do(sup, "POST", e.wpath("/orders"), map[string]any{
			"ref": fmt.Sprintf("SO-%d", i), "lines": []map[string]any{{"sku": e.w.SKUs[0], "qty": 1}},
		})
		expect(t, st, b, 201)
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		st, b := e.do(sup, "GET", e.wpath("/orders?limit=2&cursor="+cursor), nil)
		expect(t, st, b, 200)
		var pg struct {
			Items      []struct{ ID string }
			NextCursor string `json:"next_cursor"`
		}
		mustJSON(t, b, &pg)
		for _, it := range pg.Items {
			if seen[it.ID] {
				t.Fatalf("order %s repeated across pages", it.ID)
			}
			seen[it.ID] = true
		}
		if pg.NextCursor == "" {
			break
		}
		cursor = pg.NextCursor
	}
	if len(seen) != 5 {
		t.Errorf("paged through %d orders, want 5", len(seen))
	}
	if st, _ := e.do(sup, "GET", e.wpath("/orders?cursor=!!!"), nil); st != 400 {
		t.Errorf("bad cursor = %d, want 400", st)
	}
}
