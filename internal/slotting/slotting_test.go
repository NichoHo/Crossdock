package slotting_test

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbound"
	"github.com/nicholasgasior/crossdock/internal/slotting"
	"github.com/nicholasgasior/crossdock/internal/tasks"
	"github.com/nicholasgasior/crossdock/internal/testdb"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

// Floor used by these tests: aisle 1, bays 1 (next to the dock) to 30 (far end).
const near, mid, far = "A01-01-A-1", "A01-10-A-1", "A01-30-A-1"

type rig struct {
	t     *testing.T
	ctx   context.Context
	db    *sql.DB
	w     wmstest.World
	store *inventory.Store
	svc   *slotting.Service
	clock time.Time
	lots  map[string]inventory.LotID
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	r := &rig{t: t, ctx: context.Background(), db: db, w: wmstest.Seed(t, db, 3), store: store,
		svc: slotting.NewService(db, store), clock: time.Now(), lots: map[string]inventory.LotID{}}
	r.svc.SetClock(func() time.Time { return r.clock })
	for _, c := range []inventory.LocationCode{mid, far} {
		wmstest.AddPickLocation(t, db, r.w, c, 1000)
	}
	// The seeded pick face is A01-01-A-1 ("near"). SKU-0001 is popular but sits far; SKU-0002 is rare and sits near.
	r.lots["SKU-0001"] = wmstest.Stock(t, db, store, r.w, r.w.SKUs[0], far, 100)
	r.lots["SKU-0002"] = wmstest.Stock(t, db, store, r.w, r.w.SKUs[1], inventory.LocationCode(near), 100)
	t.Cleanup(func() { wmstest.CheckInvariants(t, db, store, r.w.WID) })
	return r
}

// pick records one completed pick task in a wave, as history, with the stock
// movements a real pick makes (one unit is received first so stock is unchanged).
func (r *rig) pick(wave, sku, loc string, at time.Time) {
	r.t.Helper()
	lot := r.lots[sku]
	taskID := ids.New("tsk")
	if _, err := r.db.Exec(`
		INSERT INTO tasks (id, warehouse_id, kind, state, sku_id, lot_id, from_location, qty, order_id, wave_id, ref, created_at, updated_at, completed_at)
		VALUES ($1,$2,'pick','completed',$3,$4,$5,1,$6,$7,$8,$9,$9,$9)`,
		taskID, string(r.w.WID), sku, string(lot), loc, "ord-"+wave, wave, ids.New("h"), at); err != nil {
		r.t.Fatal(err)
	}
	l := inventory.LocationCode(loc)
	steps := []inventory.Movement{
		{Kind: inventory.KindReceive, FromLocation: inventory.VirtualSupplier, FromState: inventory.StateAvailable, ToLocation: l, ToState: inventory.StateAvailable},
		{Kind: inventory.KindReserve, FromLocation: l, FromState: inventory.StateAvailable, ToLocation: l, ToState: inventory.StateReserved},
		{Kind: inventory.KindPick, FromLocation: l, FromState: inventory.StateReserved, ToLocation: l, ToState: inventory.StateInTransit, TaskID: taskID},
		{Kind: inventory.KindShip, FromLocation: l, FromState: inventory.StateInTransit, ToLocation: inventory.VirtualCarrier, ToState: inventory.StateAvailable},
	}
	for n, m := range steps {
		m.ID, m.WarehouseID, m.SKUID, m.LotID, m.Qty = ids.New("mvt"), r.w.WID, inventory.SKUID(sku), lot, 1
		m.OccurredAt, m.IdempotencyKey = at, fmt.Sprintf("%s-%d", taskID, n)
		if err := r.store.AppendMovement(r.ctx, m); err != nil {
			r.t.Fatal(err)
		}
	}
}

// history: n waves, each picking the popular far SKU and the rare near one.
func (r *rig) history(n int) {
	for i := 0; i < n; i++ {
		w := fmt.Sprintf("wav-%03d", i)
		at := r.clock.Add(-time.Duration(n-i) * time.Hour)
		r.pick(w, "SKU-0001", far, at)
		if i%10 == 0 {
			r.pick(w, "SKU-0002", near, at)
		}
	}
}

// proposal writes what the Python job would: swap the two SKUs.
func (r *rig) proposal(swap bool) string {
	r.t.Helper()
	id := ids.New("prop")
	if _, err := r.db.Exec(`INSERT INTO slotting_proposals (id, warehouse_id, window_days, max_moves, solver, solver_status, objective_before, objective_after, created_at)
		VALUES ($1,$2,90,10,'cpsat','OPTIMAL',1000,400,$3)`, id, string(r.w.WID), r.clock); err != nil {
		r.t.Fatal(err)
	}
	to1, to2 := near, far // moves SKU-0001 next to the dock
	if !swap {
		to1, to2 = far, near // "proposal" that changes nothing useful
	}
	for i, l := range []struct{ sku, cur, to string }{{"SKU-0001", far, to1}, {"SKU-0002", near, to2}} {
		if _, err := r.db.Exec(`INSERT INTO slotting_layout (warehouse_id, proposal_id, sku_id, current_location, proposed_location, velocity) VALUES ($1,$2,$3,$4,$5,1)`,
			string(r.w.WID), id, l.sku, l.cur, l.to); err != nil {
			r.t.Fatal(err)
		}
		if l.cur != l.to {
			if _, err := r.db.Exec(`INSERT INTO slotting_moves (warehouse_id, proposal_id, seq, sku_id, from_location, to_location) VALUES ($1,$2,$3,$4,$5,$6)`,
				string(r.w.WID), id, i+1, l.sku, l.cur, l.to); err != nil {
				r.t.Fatal(err)
			}
		}
	}
	return id
}

func (r *rig) get(id string) slotting.Proposal {
	r.t.Helper()
	p, err := r.svc.Get(r.ctx, r.w.WID, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return p
}

func (r *rig) onHand(loc string, sku inventory.SKUID) int64 {
	var n int64
	_ = r.db.QueryRow(`SELECT COALESCE(SUM(available+reserved),0) FROM positions WHERE warehouse_id=$1 AND location=$2 AND sku_id=$3`, string(r.w.WID), loc, string(sku)).Scan(&n)
	return n
}

// ---------------------------------------------------------------------------

func TestReplayMeasuresWithTheRouterNotTheSolver(t *testing.T) {
	r := newRig(t)
	r.history(30)
	id := r.proposal(true)

	res, err := r.svc.Replay(r.ctx, r.w.WID, id)
	if err != nil {
		t.Fatal(err)
	}
	// Recompute by hand with the same router: every wave picks SKU-0001 (and every tenth SKU-0002).
	var wantBefore, wantAfter float64
	for i := 0; i < 30; i++ {
		var b, a []string
		b, a = append(b, far), append(a, near)
		if i%10 == 0 {
			b, a = append(b, near), append(a, far)
		}
		_, mb, _ := outbound.Route(b)
		_, ma, _ := outbound.Route(a)
		wantBefore, wantAfter = wantBefore+mb, wantAfter+ma
	}
	if res.Waves != 30 || math.Abs(res.BeforeM-wantBefore) > 1e-6 || math.Abs(res.AfterM-wantAfter) > 1e-6 {
		t.Fatalf("replay = %+v, want 30 waves, %.1f m before, %.1f m after", res, wantBefore, wantAfter)
	}
	if res.Saving() <= 0 {
		t.Errorf("saving = %.1f m, want positive: the popular SKU moved next to the dock", res.Saving())
	}
	p := r.get(id)
	if p.State != "replayed" || p.ReplayBeforeM == nil || *p.SavingM() != res.Saving() {
		t.Errorf("stored proposal = %+v", p)
	}
	// The solver's own objective is stored but is not what the saving is made of.
	if p.ObjectiveBefore != 1000 || p.ObjectiveAfter != 400 {
		t.Errorf("solver objective changed: %v %v", p.ObjectiveBefore, p.ObjectiveAfter)
	}
}

func TestReplayIgnoresPicksOutsideTheWindowAndNeedsHistory(t *testing.T) {
	r := newRig(t)
	id := r.proposal(true)
	if _, err := r.svc.Replay(r.ctx, r.w.WID, id); err == nil {
		t.Fatal("replayed with no pick history")
	}
	// A wave 200 days old is outside the 90 day window.
	r.pick("wav-old", "SKU-0001", far, r.clock.Add(-200*24*time.Hour))
	if _, err := r.svc.Replay(r.ctx, r.w.WID, id); err == nil {
		t.Fatal("replayed a wave outside the window")
	}
}

func TestAcceptNeedsAReplayedPositiveSaving(t *testing.T) {
	r := newRig(t)
	r.history(30)

	good := r.proposal(true)
	if _, err := r.svc.Accept(r.ctx, r.w.WID, good, "sue"); err == nil {
		t.Fatal("accepted a proposal that was never replayed")
	}
	// A "proposal" that leaves things where they are saves nothing: it cannot be accepted.
	flat := r.proposal(false)
	if _, err := r.svc.Replay(r.ctx, r.w.WID, flat); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Accept(r.ctx, r.w.WID, flat, "sue"); err == nil {
		t.Fatal("accepted a proposal whose replayed saving is not positive")
	}
	if err := r.svc.Reject(r.ctx, r.w.WID, flat, "no gain"); err != nil {
		t.Fatal(err)
	}
	if r.get(flat).State != "rejected" {
		t.Errorf("state = %s, want rejected", r.get(flat).State)
	}
}

func TestAcceptedProposalBecomesRelocateTasksAndMovesTheStock(t *testing.T) {
	r := newRig(t)
	r.history(30)
	id := r.proposal(true)
	if _, err := r.svc.Replay(r.ctx, r.w.WID, id); err != nil {
		t.Fatal(err)
	}
	n, err := r.svc.Accept(r.ctx, r.w.WID, id, "sue")
	if err != nil || n != 2 {
		t.Fatalf("Accept = %d, %v; want 2 relocate tasks", n, err)
	}
	if again, err := r.svc.Accept(r.ctx, r.w.WID, id, "sue"); err != nil || again != 2 {
		t.Errorf("repeating Accept = %d, %v; want a no-op that reports the same 2", again, err)
	}
	if err := r.svc.Reject(r.ctx, r.w.WID, id, "changed my mind"); err == nil {
		t.Error("rejected an accepted proposal")
	}
	ts, _ := tasks.List(r.ctx, r.db, r.w.WID, tasks.Filter{Kind: inventory.TaskRelocate}, time.Time{}, "", 10)
	if len(ts) != 2 {
		t.Fatalf("relocate tasks = %d", len(ts))
	}
	for _, tk := range ts {
		if tk.Reason != "slotting" || tk.Qty != 100 {
			t.Errorf("task = %+v", tk)
		}
	}

	// Do the moves. SKU-0001 first (seq 1), then SKU-0002 into the slot it vacated.
	for _, tk := range ts {
		if err := r.svc.CompleteRelocate(r.ctx, r.w.WID, tk.ID); err != nil {
			t.Fatalf("relocate %s: %v", tk.ID, err)
		}
		if err := r.svc.CompleteRelocate(r.ctx, r.w.WID, tk.ID); err != nil {
			t.Fatalf("repeat relocate: %v", err)
		}
	}
	if got := r.onHand(near, r.w.SKUs[0]); got != 100 {
		t.Errorf("popular SKU next to the dock holds %d, want 100", got)
	}
	if got := r.onHand(far, r.w.SKUs[1]); got != 100 {
		t.Errorf("rare SKU at the far end holds %d, want 100", got)
	}
	if r.onHand(far, r.w.SKUs[0])+r.onHand(near, r.w.SKUs[1]) != 0 {
		t.Error("stock was left behind")
	}
	if got := r.get(id).State; got != "completed" {
		t.Errorf("proposal state = %s, want completed", got)
	}
}

func TestOnlyAvailableStockIsMoved(t *testing.T) {
	r := newRig(t)
	r.history(30)
	// 40 units of SKU-0001 are reserved for an order and must not be relocated.
	err := r.store.AppendMovement(r.ctx, inventory.Movement{
		ID: ids.New("mvt"), WarehouseID: r.w.WID, Kind: inventory.KindReserve,
		FromLocation: far, FromState: inventory.StateAvailable, ToLocation: far, ToState: inventory.StateReserved,
		SKUID: r.w.SKUs[0], LotID: r.lots["SKU-0001"], Qty: 40, OccurredAt: time.Now(), IdempotencyKey: "res-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := r.proposal(true)
	if _, err := r.svc.Replay(r.ctx, r.w.WID, id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Accept(r.ctx, r.w.WID, id, "sue"); err != nil {
		t.Fatal(err)
	}
	ts, _ := tasks.List(r.ctx, r.db, r.w.WID, tasks.Filter{Kind: inventory.TaskRelocate}, time.Time{}, "", 10)
	for _, tk := range ts {
		if tk.SKU == r.w.SKUs[0] && tk.Qty != 60 {
			t.Errorf("relocate qty for the SKU with 40 reserved = %d, want 60", tk.Qty)
		}
		if err := r.svc.CompleteRelocate(r.ctx, r.w.WID, tk.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.onHand(far, r.w.SKUs[0]); got != 40 {
		t.Errorf("reserved units left at the old location = %d, want 40", got)
	}
}

func TestReplayPendingMeasuresWhatTheJobWrote(t *testing.T) {
	r := newRig(t)
	r.history(10)
	id := r.proposal(true)
	n, err := r.svc.ReplayPending(r.ctx, r.w.WID)
	if err != nil || n != 1 {
		t.Fatalf("ReplayPending = %d, %v", n, err)
	}
	if r.get(id).State != "replayed" {
		t.Error("proposal was not replayed")
	}
	if n, _ := r.svc.ReplayPending(r.ctx, r.w.WID); n != 0 {
		t.Errorf("a second pass replayed %d", n)
	}
}

func TestRealisedComparesWavesBeforeAndAfter(t *testing.T) {
	r := newRig(t)
	r.history(30)
	id := r.proposal(true)
	_, _ = r.svc.Replay(r.ctx, r.w.WID, id)
	_, _ = r.svc.Accept(r.ctx, r.w.WID, id, "sue")
	// 5 real waves before the moves (long routes), the moves, then 5 after (short).
	insertWave := func(m float64, at time.Time) {
		if _, err := r.db.Exec(`INSERT INTO waves (id, warehouse_id, route_distance, created_at) VALUES ($1,$2,$3,$4)`, ids.New("wav"), string(r.w.WID), m, at); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		insertWave(100, r.clock.Add(-time.Duration(i+1)*time.Hour))
	}
	ts, _ := tasks.List(r.ctx, r.db, r.w.WID, tasks.Filter{Kind: inventory.TaskRelocate}, time.Time{}, "", 10)
	r.clock = r.clock.Add(time.Hour)
	for _, tk := range ts {
		if err := r.svc.CompleteRelocate(r.ctx, r.w.WID, tk.ID); err != nil {
			t.Fatal(err)
		}
	}
	// completed_at is database time; make the later waves clearly after it.
	for i := 0; i < 5; i++ {
		insertWave(60, time.Now().Add(time.Duration(i+1)*time.Minute))
	}
	got, err := r.svc.Realised(r.ctx, r.w.WID, r.get(id))
	if err != nil {
		t.Fatal(err)
	}
	if got.WavesBefore != 5 || got.WavesAfter != 5 || got.AvgBeforeM != 100 || got.AvgAfterM != 60 {
		t.Fatalf("realised = %+v, want 5 waves averaging 100 m before and 5 averaging 60 m after", got)
	}
}

func TestFloorMapCountsPicksPerLocation(t *testing.T) {
	r := newRig(t)
	r.history(30) // 30 picks at far, 3 at near
	cells, err := r.svc.FloorMap(r.ctx, r.w.WID, 90)
	if err != nil {
		t.Fatal(err)
	}
	picks := map[string]int{}
	zones := map[string]string{}
	for _, c := range cells {
		picks[c.Code], zones[c.Code] = c.Picks, c.Zone
	}
	if picks[far] != 30 || picks[near] != 3 || picks[mid] != 0 {
		t.Errorf("picks far/near/mid = %d/%d/%d, want 30/3/0", picks[far], picks[near], picks[mid])
	}
	if zones[far] != "pick_face" || zones[string(r.w.Dock)] != "receiving" {
		t.Errorf("zones = %v", zones)
	}
}
