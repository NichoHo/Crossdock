package counting_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/nicholasgasior/crossdock/internal/counting"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/tasks"
	"github.com/nicholasgasior/crossdock/internal/testdb"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

type rig struct {
	t     *testing.T
	ctx   context.Context
	db    *sql.DB
	w     wmstest.World
	store *inventory.Store
	svc   *counting.Service
	clock time.Time
	lot   inventory.LotID
}

// newRig seeds a warehouse with `stock` units of SKU 0 at the pick-face location.
// Every test ends by checking the stock invariants.
func newRig(t *testing.T, stock int64) *rig {
	t.Helper()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	w := wmstest.Seed(t, db, 2)
	r := &rig{t: t, ctx: context.Background(), db: db, w: w, store: store, svc: counting.NewService(db, store), clock: time.Now()}
	r.svc.SetClock(func() time.Time { return r.clock })
	if stock > 0 {
		r.lot = wmstest.Stock(t, db, store, w, w.SKUs[0], w.Pick, stock)
	}
	t.Cleanup(func() { wmstest.CheckInvariants(t, db, store, w.WID) })
	return r
}

func (r *rig) sku() string { return string(r.w.SKUs[0]) }

// countTask creates an open count task for the pick-face location.
func (r *rig) countTask(ref string) string {
	r.t.Helper()
	tk, _, err := tasks.Create(r.ctx, r.db, tasks.Task{WarehouseID: r.w.WID, Kind: inventory.TaskCount, FromLocation: r.w.Pick, Ref: ref, Reason: "schedule"})
	if err != nil {
		r.t.Fatal(err)
	}
	return tk.ID
}

func (r *rig) line(counted, damaged int64) []counting.Line {
	return []counting.Line{{SKU: r.sku(), Lot: string(r.lot), Counted: counted, Damaged: damaged}}
}

func (r *rig) submit(taskID string, lines []counting.Line) counting.Result {
	r.t.Helper()
	res, err := r.svc.Submit(r.ctx, r.w.WID, taskID, "olga", lines)
	if err != nil {
		r.t.Fatalf("submit: %v", err)
	}
	return res
}

// firstCount runs a first count and returns the result.
func (r *rig) firstCount(counted, damaged int64) counting.Result {
	r.t.Helper()
	tid := r.countTask(ids.New("c"))
	if err := r.svc.Start(r.ctx, r.w.WID, tid, "olga"); err != nil {
		r.t.Fatal(err)
	}
	return r.submit(tid, r.line(counted, damaged))
}

// recount finishes the recount task a first count asked for.
func (r *rig) recount(res counting.Result, counted int64) counting.Result {
	r.t.Helper()
	if res.Recount == "" {
		r.t.Fatal("no recount was requested")
	}
	if err := r.svc.Start(r.ctx, r.w.WID, res.Recount, "omar"); err != nil {
		r.t.Fatal(err)
	}
	return r.submit(res.Recount, r.line(counted, 0))
}

func (r *rig) variance(id string) counting.Variance {
	r.t.Helper()
	v, err := r.svc.GetVariance(r.ctx, r.w.WID, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return v
}

func (r *rig) onShelf(state string) int64 {
	var n int64
	if err := r.db.QueryRow(`SELECT COALESCE(SUM(`+state+`),0) FROM positions WHERE warehouse_id=$1 AND location=$2`, string(r.w.WID), string(r.w.Pick)).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) adjustments() int {
	var n int
	_ = r.db.QueryRow(`SELECT count(*) FROM movements WHERE warehouse_id=$1 AND kind='count_adjust'`, string(r.w.WID)).Scan(&n)
	return n
}

// relocateAway moves n units off the shelf, as a pick or a move would.
func (r *rig) relocateAway(n int64) {
	r.t.Helper()
	wmstest.AddPickLocation(r.t, r.db, r.w, "A02-01-A-1", 100)
	err := r.store.AppendMovement(r.ctx, inventory.Movement{
		ID: ids.New("mvt"), WarehouseID: r.w.WID, Kind: inventory.KindRelocate,
		FromLocation: r.w.Pick, FromState: inventory.StateAvailable, ToLocation: "A02-01-A-1", ToState: inventory.StateAvailable,
		SKUID: r.w.SKUs[0], LotID: r.lot, Qty: n, OccurredAt: time.Now(), IdempotencyKey: ids.New("rel"),
	})
	if err != nil {
		r.t.Fatal(err)
	}
}

func oneVariance(t *testing.T, res counting.Result) string {
	t.Helper()
	if len(res.Variances) != 1 {
		t.Fatalf("variances = %v, want exactly one", res.Variances)
	}
	return res.Variances[0]
}

// ---------------------------------------------------------------------------
// Fixture scenarios: one per row of the table in spec 8.2.
// ---------------------------------------------------------------------------

func TestCountMatchesSystem(t *testing.T) {
	r := newRig(t, 10)
	res := r.firstCount(10, 0)
	if len(res.Variances) != 0 || res.Matched != 1 || res.Recount != "" {
		t.Fatalf("result = %+v, want a clean match", res)
	}
}

func TestTimingExplainedByInFlightMovement(t *testing.T) {
	r := newRig(t, 10)
	tid := r.countTask("c1")
	if err := r.svc.Start(r.ctx, r.w.WID, tid, "olga"); err != nil {
		t.Fatal(err)
	}
	r.relocateAway(3) // a pick takes 3 while the operator is counting; they saw 10
	res := r.submit(tid, r.line(10, 0))

	v := r.variance(oneVariance(t, res))
	// The system now holds 7; the operator saw 10 before the pick took 3.
	if v.Proposed != counting.Timing || v.State != "open" || v.SystemQty != 7 || v.CountedQty != 10 || v.Variance != -3 {
		t.Fatalf("variance = %+v, want timing with system 7, counted 10", v)
	}
	if res.Recount != "" {
		t.Error("a timing difference must not ask for a recount")
	}
	if err := r.svc.Confirm(r.ctx, r.w.WID, v.ID, counting.Timing, "", "sue"); err != nil {
		t.Fatal(err)
	}
	if r.adjustments() != 0 {
		t.Error("timing must not post an adjustment")
	}
	if got := r.variance(v.ID); got.State != "closed" {
		t.Errorf("state = %s, want closed", got.State)
	}
}

func TestShrinkageNeedsRecountThenSupervisor(t *testing.T) {
	r := newRig(t, 10)
	res := r.firstCount(7, 0)
	v := r.variance(oneVariance(t, res))
	if v.State != "awaiting_recount" || res.Recount == "" {
		t.Fatalf("first count of an unexplained loss: state %s, recount %q; want awaiting_recount with a recount task", v.State, res.Recount)
	}
	// Nothing is adjusted on one count.
	if err := r.svc.Confirm(r.ctx, r.w.WID, v.ID, counting.Shrinkage, "", "sue"); err == nil {
		t.Fatal("confirmed an adjustment before the recount")
	}

	r.recount(res, 7)
	v = r.variance(v.ID)
	if v.Proposed != counting.Shrinkage || !v.Flagged || v.Variance != 3 || v.State != "open" {
		t.Fatalf("after a recount that agrees: %+v, want shrinkage of 3, flagged, open", v)
	}
	if r.adjustments() != 0 {
		t.Fatal("an adjustment posted before the supervisor confirmed")
	}
	if err := r.svc.Confirm(r.ctx, r.w.WID, v.ID, counting.Shrinkage, "", "sue"); err != nil {
		t.Fatal(err)
	}
	if got := r.onShelf("available"); got != 7 {
		t.Errorf("available after the adjustment = %d, want 7", got)
	}
	var carrier int64
	_ = r.db.QueryRow(`SELECT COALESCE(SUM(available),0) FROM positions WHERE warehouse_id=$1 AND location='VARIANCE'`, string(r.w.WID)).Scan(&carrier)
	if carrier != 3 {
		t.Errorf("VARIANCE holds %d, want +3 (the units that left the books)", carrier)
	}
	got := r.variance(v.ID)
	if got.State != "closed" || got.AdjustmentMovement == "" || got.ConfirmedBy != "sue" {
		t.Errorf("closed variance = %+v", got)
	}
	// The same decision repeated changes nothing.
	if err := r.svc.Confirm(r.ctx, r.w.WID, v.ID, counting.Shrinkage, "", "sue"); err != nil {
		t.Errorf("repeat confirm: %v", err)
	}
	if r.adjustments() != 1 {
		t.Errorf("adjustments = %d, want exactly 1", r.adjustments())
	}
}

func TestFoundStockIsAdjustedInFromVariance(t *testing.T) {
	r := newRig(t, 10)
	res := r.firstCount(12, 0)
	r.recount(res, 12)
	v := r.variance(oneVariance(t, res))
	if v.Proposed != counting.Found || v.Variance != -2 {
		t.Fatalf("variance = %+v, want found, variance -2", v)
	}
	if err := r.svc.Confirm(r.ctx, r.w.WID, v.ID, counting.Found, "", "sue"); err != nil {
		t.Fatal(err)
	}
	if got := r.onShelf("available"); got != 12 {
		t.Errorf("available = %d, want 12", got)
	}
}

func TestMiscountIsDiscarded(t *testing.T) {
	r := newRig(t, 10)
	res := r.firstCount(6, 0)
	r.recount(res, 10) // the second look returns the system quantity
	v := r.variance(oneVariance(t, res))
	if v.Proposed != counting.Miscount || v.Flagged {
		t.Fatalf("variance = %+v, want miscount", v)
	}
	if err := r.svc.Confirm(r.ctx, r.w.WID, v.ID, counting.Miscount, "", "sue"); err != nil {
		t.Fatal(err)
	}
	if r.adjustments() != 0 || r.onShelf("available") != 10 {
		t.Error("a miscount must change nothing")
	}
}

func TestDamagedUnitsAreQuarantinedNotAdjusted(t *testing.T) {
	r := newRig(t, 10)
	res := r.firstCount(10, 2) // all 10 are there, 2 are broken
	v := r.variance(oneVariance(t, res))
	if v.Proposed != counting.Damaged || v.DamagedQty != 2 || res.Recount != "" {
		t.Fatalf("variance = %+v, recount %q; want damaged x2, no recount", v, res.Recount)
	}
	if err := r.svc.Confirm(r.ctx, r.w.WID, v.ID, counting.Damaged, "", "sue"); err != nil {
		t.Fatal(err)
	}
	if r.adjustments() != 0 {
		t.Error("damaged must be a quarantine movement, not an adjustment")
	}
	if a, q := r.onShelf("available"), r.onShelf("quarantined"); a != 8 || q != 2 {
		t.Errorf("available %d quarantined %d, want 8 and 2", a, q)
	}
}

func TestUnresolvedStaysOpenAndAges(t *testing.T) {
	r := newRig(t, 10)
	res := r.firstCount(6, 0)
	r.recount(res, 8) // disagrees with both the first count and the system
	v := r.variance(oneVariance(t, res))
	if v.Proposed != counting.Unresolved {
		t.Fatalf("proposed = %s, want unresolved", v.Proposed)
	}
	if err := r.svc.Confirm(r.ctx, r.w.WID, v.ID, counting.Unresolved, "chasing the cycle counter", "sue"); err != nil {
		t.Fatal(err)
	}
	if got := r.variance(v.ID); got.State != "open" || got.Classification != counting.Unresolved {
		t.Fatalf("variance = %+v, want it to stay open", got)
	}
	// It ages into the buckets (spec 8.4).
	r.clock = r.clock.Add(100 * 24 * time.Hour)
	ag, err := r.svc.Ageing(r.ctx, r.w.WID)
	if err != nil || ag.Over90 != 1 || ag.Total() != 1 {
		t.Fatalf("ageing = %+v, %v; want one item over 90 days", ag, err)
	}
}

func TestRecountAfterAnHourCannotClassify(t *testing.T) {
	r := newRig(t, 10)
	res := r.firstCount(6, 0)
	r.clock = r.clock.Add(counting.RecountWindow + time.Minute)
	r.recount(res, 6) // agrees, but too late to trust
	v := r.variance(oneVariance(t, res))
	if v.Proposed != counting.Unresolved {
		t.Fatalf("proposed = %s, want unresolved for a late recount", v.Proposed)
	}
}

func TestOverridingTheProposalNeedsANote(t *testing.T) {
	r := newRig(t, 10)
	res := r.firstCount(7, 0)
	r.recount(res, 7)
	v := r.variance(oneVariance(t, res))
	if err := r.svc.Confirm(r.ctx, r.w.WID, v.ID, counting.Miscount, "", "sue"); err == nil {
		t.Fatal("overrode the proposal without a note")
	}
	if err := r.svc.Confirm(r.ctx, r.w.WID, v.ID, counting.Miscount, "recounted a third time, it is 10", "sue"); err != nil {
		t.Fatal(err)
	}
	if r.adjustments() != 0 {
		t.Error("the override must not post an adjustment")
	}
}

func TestAnAdjustmentCannotPostWithoutSupportingNumbers(t *testing.T) {
	r := newRig(t, 10)
	res := r.firstCount(6, 0)
	r.recount(res, 10) // miscount: variance 0
	v := r.variance(oneVariance(t, res))
	if err := r.svc.Confirm(r.ctx, r.w.WID, v.ID, counting.Shrinkage, "force it", "sue"); err == nil {
		t.Fatal("posted a shrinkage with no variance")
	}
	if err := r.svc.Confirm(r.ctx, r.w.WID, v.ID, counting.Class("whatever"), "", "sue"); err == nil {
		t.Fatal("accepted an unknown classification")
	}
}

func TestSubmitIsIdempotent(t *testing.T) {
	r := newRig(t, 10)
	tid := r.countTask("c1")
	_ = r.svc.Start(r.ctx, r.w.WID, tid, "olga")
	first := r.submit(tid, r.line(7, 0))
	again := r.submit(tid, r.line(7, 0))
	if again.CountID != first.CountID {
		t.Errorf("second submit made a new count: %s vs %s", again.CountID, first.CountID)
	}
	var n int
	_ = r.db.QueryRow(`SELECT count(*) FROM variances WHERE warehouse_id=$1`, string(r.w.WID)).Scan(&n)
	if n != 1 {
		t.Errorf("variances = %d after a repeated submit, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// Scheduling (spec 8.1)
// ---------------------------------------------------------------------------

func TestScheduleByVelocityClass(t *testing.T) {
	r := newRig(t, 10) // SKU-0001 is class A
	n, err := r.svc.ScheduleCounts(r.ctx, r.w.WID)
	if err != nil || n != 1 {
		t.Fatalf("first schedule = %d, %v; want one task for the never-counted location", n, err)
	}
	if n, _ := r.svc.ScheduleCounts(r.ctx, r.w.WID); n != 0 {
		t.Errorf("scheduling twice created %d more", n)
	}
	// Count it. An A-class location is not due again for a week.
	tk, _ := tasks.List(r.ctx, r.db, r.w.WID, tasks.Filter{Kind: inventory.TaskCount}, time.Time{}, "", 10)
	if len(tk) != 1 || tk[0].FromLocation != r.w.Pick || tk[0].Reason != "schedule" {
		t.Fatalf("tasks = %+v", tk)
	}
	r.submit(tk[0].ID, r.line(10, 0))

	r.clock = r.clock.Add(6 * 24 * time.Hour)
	if n, _ := r.svc.ScheduleCounts(r.ctx, r.w.WID); n != 0 {
		t.Errorf("an A location was due after 6 days (created %d)", n)
	}
	r.clock = r.clock.Add(2 * 24 * time.Hour)
	if n, _ := r.svc.ScheduleCounts(r.ctx, r.w.WID); n != 1 {
		t.Errorf("an A location was not due after 8 days (created %d)", n)
	}
}

func TestSlowMoversAreCountedLessOften(t *testing.T) {
	r := newRig(t, 0) // no stock at the pick location
	if _, err := r.db.Exec(`UPDATE skus SET velocity_class='C' WHERE warehouse_id=$1`, string(r.w.WID)); err != nil {
		t.Fatal(err)
	}
	wmstest.Stock(t, r.db, r.store, r.w, r.w.SKUs[1], r.w.Bulk, 5)
	r.clock = r.clock.Add(0)
	n, _ := r.svc.ScheduleCounts(r.ctx, r.w.WID)
	if n != 1 {
		t.Fatalf("created %d, want 1 (the bulk location)", n)
	}
	tk, _ := tasks.List(r.ctx, r.db, r.w.WID, tasks.Filter{Kind: inventory.TaskCount}, time.Time{}, "", 10)
	r.submit(tk[0].ID, []counting.Line{{SKU: string(r.w.SKUs[1]), Lot: "", Counted: 5}})

	r.clock = r.clock.Add(60 * 24 * time.Hour)
	if n, _ := r.svc.ScheduleCounts(r.ctx, r.w.WID); n != 0 {
		t.Errorf("a C location was due after 60 days (created %d)", n)
	}
	r.clock = r.clock.Add(40 * 24 * time.Hour)
	if n, _ := r.svc.ScheduleCounts(r.ctx, r.w.WID); n != 1 {
		t.Errorf("a C location was not due after 100 days (created %d)", n)
	}
}

func TestExceptionTouchedLocationsAreCountedSooner(t *testing.T) {
	r := newRig(t, 10)
	// Count it so it is not due by schedule.
	r.firstCount(10, 0)
	if n, _ := r.svc.ScheduleCounts(r.ctx, r.w.WID); n != 0 {
		t.Fatalf("created %d for a freshly counted location", n)
	}
	// A short pick at that location.
	tk, _, _ := tasks.Create(r.ctx, r.db, tasks.Task{WarehouseID: r.w.WID, Kind: inventory.TaskPick, FromLocation: r.w.Pick, Qty: 1, Ref: "alc-x"})
	if err := tasks.Raise(r.ctx, r.db, r.w.WID, tk.ID, "short_pick: nothing found"); err != nil {
		t.Fatal(err)
	}
	n, err := r.svc.ScheduleCounts(r.ctx, r.w.WID)
	if err != nil || n != 1 {
		t.Fatalf("schedule after a short pick = %d, %v; want 1", n, err)
	}
	var reason string
	_ = r.db.QueryRow(`SELECT reason FROM tasks WHERE warehouse_id=$1 AND kind='count' AND state='open' AND ref LIKE 'count-exc:%'`, string(r.w.WID)).Scan(&reason)
	if reason != "exception" {
		t.Errorf("task reason = %q, want exception", reason)
	}
	if n, _ := r.svc.ScheduleCounts(r.ctx, r.w.WID); n != 0 {
		t.Errorf("the exception count was scheduled twice (%d)", n)
	}
}
