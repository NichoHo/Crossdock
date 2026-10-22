package api_test

import (
	"fmt"
	"testing"

	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

// TestChaosScannerSyncsFiftyQueuedCommands (spec 14.7): a scanner that was
// offline syncs fifty queued commands at once. Every command comes back, in the
// order it was queued, as applied or rejected with a reason; a second sync
// changes nothing.
func TestChaosScannerSyncsFiftyQueuedCommands(t *testing.T) {
	e := newEnv(t)
	sup := user("sue", auth.RoleSupervisor)
	op := user("olga", auth.RoleOperator)
	st, b := e.do(sup, "POST", e.wpath("/asns"), map[string]any{
		"ref": "PO-OFFLINE", "lines": []map[string]any{{"sku": e.w.SKUs[0], "expected_qty": 100}},
	})
	expect(t, st, b, 201)
	var asn struct{ Lines []struct{ ID string } }
	mustJSON(t, b, &asn)

	// Every fifth command is bad: it names a task that does not exist.
	var queue []map[string]any
	for i := range 50 {
		if i%5 == 4 {
			queue = append(queue, map[string]any{"key": fmt.Sprintf("q-%02d", i), "type": "pick", "payload": map[string]any{"task_id": "nope"}})
			continue
		}
		queue = append(queue, map[string]any{"key": fmt.Sprintf("q-%02d", i), "type": "receive",
			"payload": map[string]any{"asn_line_id": asn.Lines[0].ID, "qty": 1, "location": e.w.Dock}})
	}

	type result struct {
		Key, Status, Reason string
		Code                int
	}
	sync := func() []result {
		st, b := e.do(op, "POST", e.wpath("/commands"), map[string]any{"commands": queue})
		expect(t, st, b, 200)
		var res struct{ Results []result }
		mustJSON(t, b, &res)
		if len(res.Results) != len(queue) {
			t.Fatalf("%d results for %d commands", len(res.Results), len(queue))
		}
		return res.Results
	}

	applied, rejected := 0, 0
	for i, r := range sync() {
		if r.Key != queue[i]["key"] {
			t.Fatalf("result %d is for %q, want %q: results are out of order", i, r.Key, queue[i]["key"])
		}
		switch r.Status {
		case "applied":
			applied++
		case "rejected":
			rejected++
			if r.Reason == "" {
				t.Errorf("command %s rejected without a reason", r.Key)
			}
		default:
			t.Errorf("command %s: status %q on first sync", r.Key, r.Status)
		}
	}
	if applied != 40 || rejected != 10 {
		t.Errorf("applied %d, rejected %d; want 40 and 10", applied, rejected)
	}

	for i, r := range sync() { // the scanner never heard back and syncs again
		if r.Status != "duplicate" {
			t.Errorf("command %d on second sync: %q, want duplicate", i, r.Status)
		}
	}
	var received int64
	if err := e.db.QueryRow(`SELECT COALESCE(SUM(qty),0) FROM movements WHERE warehouse_id=$1 AND kind='receive'`, string(e.w.WID)).Scan(&received); err != nil {
		t.Fatal(err)
	}
	if received != 40 {
		t.Errorf("received %d units, want 40", received)
	}
	wmstest.CheckInvariants(t, e.db, e.store, e.w.WID)
}
