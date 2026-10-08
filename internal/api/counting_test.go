package api_test

import (
	"testing"

	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

// TestCountToAdjustmentOverHTTP drives the whole loop as the people would: the
// system schedules a count, an operator counts blind on the scanner, a recount
// agrees, and a supervisor confirms the shrinkage in the workbench.
func TestCountToAdjustmentOverHTTP(t *testing.T) {
	e := newEnv(t)
	sup, op := user("sue", auth.RoleSupervisor), user("olga", auth.RoleOperator)
	lot := wmstest.Stock(t, e.db, e.store, e.w, e.w.SKUs[0], e.w.Pick, 10)

	st, b := e.do(sup, "POST", e.wpath("/count-schedules"), nil)
	expect(t, st, b, 200)
	var sched struct{ Created int }
	mustJSON(t, b, &sched)
	if sched.Created != 1 {
		t.Fatalf("scheduled %d count tasks, want 1: %s", sched.Created, b)
	}

	// The operator claims it. The sheet lists what to look for, with no quantities.
	st, b = e.do(op, "POST", e.wpath("/tasks/claim"), map[string]any{"kinds": []string{"count"}})
	expect(t, st, b, 200)
	var task struct {
		ID, Kind, Reason string
	}
	mustJSON(t, b, &task)
	if task.Kind != "count" || task.Reason != "schedule" {
		t.Fatalf("claimed %s", b)
	}
	st, b = e.do(op, "GET", e.wpath("/tasks/"+task.ID+"/count-sheet"), nil)
	expect(t, st, b, 200)
	if contains(b, `"qty"`) || contains(b, `"available"`) {
		t.Errorf("count sheet leaks quantities: %s", b)
	}

	// An operator cannot count a task that is not theirs, nor a supervisor-only view.
	st, b = e.do(user("mallory", auth.RoleOperator), "POST", e.wpath("/commands"), map[string]any{"commands": []map[string]any{
		{"key": "x1", "type": "count_start", "payload": map[string]any{"task_id": task.ID}}}})
	expect(t, st, b, 200)
	if !contains(b, `"rejected"`) {
		t.Errorf("another operator started the count: %s", b)
	}
	st, b = e.do(op, "GET", e.wpath("/variances"), nil)
	expect(t, st, b, 403)

	// Count: 7 where the system says 10.
	count := func(key, taskID string, qty int) []byte {
		st, b := e.do(op, "POST", e.wpath("/commands"), map[string]any{"commands": []map[string]any{
			{"key": key + "-s", "type": "count_start", "payload": map[string]any{"task_id": taskID}},
			{"key": key, "type": "count", "payload": map[string]any{"task_id": taskID, "lines": []map[string]any{
				{"sku": e.w.SKUs[0], "lot": lot, "counted": qty}}}},
		}})
		expect(t, st, b, 200)
		return b
	}
	b = count("c1", task.ID, 7)
	var res struct {
		Results []struct {
			Status string
			Result struct {
				Variances int    `json:"variances"`
				Recount   string `json:"recount_task_id"`
			}
		}
	}
	mustJSON(t, b, &res)
	if len(res.Results) != 2 || res.Results[1].Status != "applied" || res.Results[1].Result.Variances != 1 || res.Results[1].Result.Recount == "" {
		t.Fatalf("count result = %s", b)
	}

	// Nothing to confirm yet: it is waiting for the recount.
	st, b = e.do(sup, "GET", e.wpath("/variances?state=awaiting_recount"), nil)
	expect(t, st, b, 200)
	var vl struct{ Items []struct{ ID, State, Proposed string } }
	mustJSON(t, b, &vl)
	if len(vl.Items) != 1 || vl.Items[0].State != "awaiting_recount" {
		t.Fatalf("variances = %s", b)
	}
	vid := vl.Items[0].ID
	st, b = e.do(sup, "POST", e.wpath("/variances/"+vid+"/confirm"), map[string]any{"classification": "shrinkage"})
	expect(t, st, b, 409)

	// A different operator recounts (assigned by claim) and agrees.
	op2 := user("omar", auth.RoleOperator)
	st, b = e.do(op2, "POST", e.wpath("/tasks/claim"), map[string]any{"kinds": []string{"count"}})
	expect(t, st, b, 200)
	var recount struct{ ID, Reason string }
	mustJSON(t, b, &recount)
	if recount.Reason != "recount" {
		t.Fatalf("second claim = %s, want the recount", b)
	}
	st, b = e.do(op2, "POST", e.wpath("/commands"), map[string]any{"commands": []map[string]any{
		{"key": "r1-s", "type": "count_start", "payload": map[string]any{"task_id": recount.ID}},
		{"key": "r1", "type": "count", "payload": map[string]any{"task_id": recount.ID, "lines": []map[string]any{
			{"sku": e.w.SKUs[0], "lot": lot, "counted": 7}}}},
	}})
	expect(t, st, b, 200)

	// The workbench shows it; confirming posts the adjustment.
	st, b = e.do(sup, "GET", e.wpath("/workbench"), nil)
	expect(t, st, b, 200)
	var wb struct{ Open int }
	mustJSON(t, b, &wb)
	if wb.Open != 1 {
		t.Fatalf("workbench = %s, want 1 open item", b)
	}
	st, b = e.do(sup, "GET", e.wpath("/variances/"+vid), nil)
	expect(t, st, b, 200)
	var v struct {
		Proposed string
		Flagged  bool
		Variance int
	}
	mustJSON(t, b, &v)
	if v.Proposed != "shrinkage" || !v.Flagged || v.Variance != 3 {
		t.Fatalf("variance = %s", b)
	}
	st, b = e.do(sup, "POST", e.wpath("/variances/"+vid+"/confirm"), map[string]any{"classification": "shrinkage"})
	expect(t, st, b, 200)
	st, b = e.do(sup, "GET", e.wpath("/workbench"), nil)
	expect(t, st, b, 200)
	mustJSON(t, b, &wb)
	if wb.Open != 0 {
		t.Errorf("workbench after confirm = %s, want nothing open", b)
	}
	var adj int
	_ = e.db.QueryRow(`SELECT count(*) FROM movements WHERE warehouse_id=$1 AND kind='count_adjust'`, string(e.w.WID)).Scan(&adj)
	if adj != 1 {
		t.Errorf("adjustments = %d, want 1", adj)
	}
	wmstest.CheckInvariants(t, e.db, e.store, e.w.WID)
}

func TestReconciliationBreaksOverHTTP(t *testing.T) {
	e := newEnv(t)
	sup, op := user("sue", auth.RoleSupervisor), user("olga", auth.RoleOperator)
	st, b := e.do(sup, "POST", e.wpath("/asns"), map[string]any{"ref": "PO-R", "supplier": "Harbour Foods",
		"lines": []map[string]any{{"sku": e.w.SKUs[0], "expected_qty": 5}}})
	expect(t, st, b, 201)
	var asn struct{ Lines []struct{ ID string } }
	mustJSON(t, b, &asn)
	st, b = e.do(op, "POST", e.wpath("/receipts"), map[string]any{"asn_line_id": asn.Lines[0].ID, "qty": 8, "location": e.w.Dock}, "Idempotency-Key", "k1")
	expect(t, st, b, 201)

	st, b = e.do(sup, "POST", e.wpath("/reconciliations"), nil)
	expect(t, st, b, 200)
	st, b = e.do(sup, "GET", e.wpath("/recon-breaks?kind=over_shipped"), nil)
	expect(t, st, b, 200)
	var bl struct {
		Items []struct {
			ID, Kind, Supplier string
			ASNRef             string `json:"asn_ref"`
		}
	}
	mustJSON(t, b, &bl)
	if len(bl.Items) != 1 || bl.Items[0].ASNRef != "PO-R" || bl.Items[0].Supplier != "Harbour Foods" {
		t.Fatalf("breaks = %s", b)
	}
	st, b = e.do(sup, "POST", e.wpath("/recon-breaks/"+bl.Items[0].ID+"/waive"), map[string]any{"note": ""})
	expect(t, st, b, 422)
	st, b = e.do(sup, "POST", e.wpath("/recon-breaks/"+bl.Items[0].ID+"/waive"), map[string]any{"note": "supplier credited the extra 3"})
	expect(t, st, b, 204)
	st, b = e.do(sup, "GET", e.wpath("/recon-breaks"), nil)
	expect(t, st, b, 200)
	mustJSON(t, b, &bl)
	for _, x := range bl.Items {
		if x.Kind == "over_shipped" {
			t.Errorf("the waived break is still listed as open: %s", b)
		}
	}
}
