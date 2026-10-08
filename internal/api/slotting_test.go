package api_test

import (
	"fmt"
	"testing"

	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

// TestSlottingProposalToRelocatedStockOverHTTP: the job writes a proposal, the
// platform replays it, a supervisor accepts it, and an operator moves the stock
// on the scanner.
func TestSlottingProposalToRelocatedStockOverHTTP(t *testing.T) {
	e := newEnv(t)
	sup, op := user("sue", auth.RoleSupervisor), user("olga", auth.RoleOperator)
	const near, far = "A01-01-A-1", "A01-30-A-1"
	wmstest.AddPickLocation(t, e.db, e.w, far, 1000)
	lot := wmstest.Stock(t, e.db, e.store, e.w, e.w.SKUs[0], far, 50)
	wmstest.Stock(t, e.db, e.store, e.w, e.w.SKUs[1], near, 50)

	// History: 20 waves, each picking the popular far SKU.
	for i := 0; i < 20; i++ {
		if _, err := e.db.Exec(`INSERT INTO tasks (id, warehouse_id, kind, state, sku_id, lot_id, from_location, qty, order_id, wave_id, ref, created_at, updated_at, completed_at)
			VALUES ($1,$2,'pick','completed',$3,$4,$5,1,$6,$7,$8,NOW(),NOW(),NOW())`,
			ids.New("tsk"), string(e.w.WID), string(e.w.SKUs[0]), string(lot), far, fmt.Sprintf("ord-%d", i), fmt.Sprintf("wav-%d", i), ids.New("h")); err != nil {
			t.Fatal(err)
		}
	}
	// What the Python job would write.
	pid := ids.New("prop")
	must := func(q string, args ...any) {
		if _, err := e.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO slotting_proposals (id, warehouse_id, window_days, max_moves, solver, solver_status, objective_before, objective_after, created_at)
		VALUES ($1,$2,90,10,'cpsat','OPTIMAL',1000,300,NOW() + interval '1 minute')`, pid, string(e.w.WID))
	for _, l := range [][3]string{{string(e.w.SKUs[0]), far, near}, {string(e.w.SKUs[1]), near, far}} {
		must(`INSERT INTO slotting_layout (warehouse_id, proposal_id, sku_id, current_location, proposed_location) VALUES ($1,$2,$3,$4,$5)`, string(e.w.WID), pid, l[0], l[1], l[2])
	}
	must(`INSERT INTO slotting_moves (warehouse_id, proposal_id, seq, sku_id, from_location, to_location) VALUES ($1,$2,1,$3,$4,$5),($1,$2,2,$6,$5,$4)`,
		string(e.w.WID), pid, string(e.w.SKUs[0]), far, near, string(e.w.SKUs[1]))

	// Before it is replayed there is no saving to show, and it cannot be accepted.
	st, b := e.do(sup, "GET", e.wpath("/slotting/proposals"), nil)
	expect(t, st, b, 200)
	if !contains(b, pid) || contains(b, `"saving_m"`) {
		t.Fatalf("list before replay = %s", b)
	}
	st, b = e.do(sup, "POST", e.wpath("/slotting/proposals/"+pid+"/accept"), nil)
	expect(t, st, b, 422)
	st, b = e.do(op, "POST", e.wpath("/slotting/proposals/"+pid+"/replay"), nil)
	expect(t, st, b, 403)

	st, b = e.do(sup, "POST", e.wpath("/slotting/proposals/"+pid+"/replay"), nil)
	expect(t, st, b, 200)
	var pr struct {
		State     string
		SavingM   float64 `json:"saving_m"`
		SavingPct float64 `json:"saving_pct"`
		Solver    map[string]float64 `json:"solver_estimate"`
		Moves     []struct{ SKU string }
	}
	mustJSON(t, b, &pr)
	if pr.State != "replayed" || pr.SavingM <= 0 || pr.SavingPct <= 0 || len(pr.Moves) != 2 || pr.Solver["before"] != 1000 {
		t.Fatalf("replayed proposal = %s", b)
	}

	st, b = e.do(sup, "POST", e.wpath("/slotting/proposals/"+pid+"/accept"), nil)
	expect(t, st, b, 202)
	st, b = e.do(sup, "GET", e.wpath("/tasks?kind=relocate&state=open"), nil)
	expect(t, st, b, 200)
	var ts struct{ Items []struct{ ID, Reason string } }
	mustJSON(t, b, &ts)
	if len(ts.Items) != 2 || ts.Items[0].Reason != "slotting" {
		t.Fatalf("relocate tasks = %s", b)
	}
	// An operator moves the stock; one who was not given the task cannot.
	for _, tk := range ts.Items {
		st, b = e.do(sup, "POST", e.wpath("/tasks/"+tk.ID+"/assign"), map[string]any{"operator": "olga"})
		expect(t, st, b, 204)
	}
	cmd := func(who caller, key, id string) []byte {
		st, b := e.do(who, "POST", e.wpath("/commands"), map[string]any{"commands": []map[string]any{
			{"key": key, "type": "relocate", "payload": map[string]any{"task_id": id}}}})
		expect(t, st, b, 200)
		return b
	}
	if b := cmd(user("mallory", auth.RoleOperator), "r-bad", ts.Items[0].ID); !contains(b, `"rejected"`) {
		t.Errorf("an unassigned operator relocated stock: %s", b)
	}
	for i, tk := range ts.Items {
		if b := cmd(op, fmt.Sprintf("r-%d", i), tk.ID); !contains(b, `"applied"`) {
			t.Fatalf("relocate = %s", b)
		}
	}
	var atNear int64
	_ = e.db.QueryRow(`SELECT COALESCE(SUM(available),0) FROM positions WHERE warehouse_id=$1 AND location=$2 AND sku_id=$3`, string(e.w.WID), near, string(e.w.SKUs[0])).Scan(&atNear)
	if atNear != 50 {
		t.Errorf("popular SKU next to the dock holds %d, want 50", atNear)
	}
	st, b = e.do(sup, "GET", e.wpath("/slotting/proposals/"+pid), nil)
	expect(t, st, b, 200)
	mustJSON(t, b, &pr)
	if pr.State != "completed" {
		t.Errorf("proposal state = %s, want completed", pr.State)
	}

	// The floor map shows where the picking happens.
	st, b = e.do(sup, "GET", e.wpath("/floor-map?days=30"), nil)
	expect(t, st, b, 200)
	var fm struct {
		Items []struct {
			Code  string
			Picks int
		}
	}
	mustJSON(t, b, &fm)
	picks := map[string]int{}
	for _, c := range fm.Items {
		picks[c.Code] = c.Picks
	}
	if picks[far] != 20 {
		t.Errorf("picks at the far location = %d, want 20 (map: %s)", picks[far], b)
	}
}
