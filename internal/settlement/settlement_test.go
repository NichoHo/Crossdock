package settlement_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/returns"
	"github.com/nicholasgasior/crossdock/internal/settlement"
	"github.com/nicholasgasior/crossdock/internal/tasks"
	"github.com/nicholasgasior/crossdock/internal/testdb"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

const carrier = "JNT"

var terms = settlement.Terms{FeeBps: 200, FeeFlat: 1000, ShippingCost: 4500, SettlementDays: 7}

type rig struct {
	t     *testing.T
	ctx   context.Context
	db    *sql.DB
	w     wmstest.World
	store *inventory.Store
	svc   *settlement.Service
	ret   *returns.Service
	clock time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	r := &rig{t: t, ctx: context.Background(), db: db, w: wmstest.Seed(t, db, 3), store: store,
		svc: settlement.NewService(db), ret: returns.NewService(db, store), clock: time.Now()}
	r.svc.SetClock(func() time.Time { return r.clock })
	r.ret.SetClock(func() time.Time { return r.clock })
	if err := r.svc.PutTerms(r.ctx, r.w.WID, carrier, terms); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { wmstest.CheckInvariants(t, db, store, r.w.WID) })
	return r
}

// shipRow inserts a dispatched shipment directly. Settlement tests that need
// many shipments use it; the one test about stock uses a real order.
func (r *rig) shipRow(tracking string, cod *int64) string {
	r.t.Helper()
	oid, sid := ids.New("ord"), ids.New("shp")
	if _, err := r.db.Exec(`INSERT INTO orders (id, warehouse_id, ref, state) VALUES ($1,$2,$3,'shipped')`, oid, string(r.w.WID), "SO-"+tracking); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.db.Exec(`INSERT INTO shipments (id, warehouse_id, order_id, carrier, tracking, cod_amount_minor, dispatched_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		sid, string(r.w.WID), oid, carrier, tracking, cod, r.clock.Add(-30*24*time.Hour)); err != nil {
		r.t.Fatal(err)
	}
	return sid
}

func (r *rig) outcome(tracking, outcome string, at time.Time) settlement.OutcomeResult {
	r.t.Helper()
	res, err := r.svc.RecordOutcome(r.ctx, r.w.WID, settlement.ShipmentRef{Tracking: tracking}, outcome, at, "courier-api")
	if err != nil {
		r.t.Fatalf("outcome %s %s: %v", tracking, outcome, err)
	}
	return res
}

func (r *rig) count(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.db.QueryRow(q, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) stock(loc inventory.LocationCode, state string) int64 {
	var n int64
	_ = r.db.QueryRow(`SELECT COALESCE(SUM(`+state+`),0) FROM positions WHERE warehouse_id=$1 AND location=$2`, string(r.w.WID), string(loc)).Scan(&n)
	return n
}

// ---------------------------------------------------------------------------
// The COD loop for one shipment (Phase 4 exit criterion, spec 9.3)
// ---------------------------------------------------------------------------

func TestRejectedParcelLoopShowsInOneTimeline(t *testing.T) {
	r := newRig(t)
	cod := int64(100000) // 1000.00
	sku := r.w.SKUs[0]
	_, shipmentID := wmstest.ShipOrder(t, r.db, r.store, r.w, sku, 3, &cod, carrier, "TRK-LOOP")
	afterShip := r.stock(r.w.Pick, "available")

	// The courier reports the parcel rejected.
	r.svc.SetClock(time.Now) // real time, so the physical and money events sort as they happened
	r.ret.SetClock(time.Now)
	res := r.outcome("TRK-LOOP", settlement.Rejected, time.Now())
	if res.ReturnAuthorisationID == "" {
		t.Fatal("a rejected outcome must open a return authorisation")
	}
	var lost int64
	_ = r.db.QueryRow(`SELECT amount_minor FROM ledger WHERE warehouse_id=$1 AND shipment_id=$2 AND kind='shipping_cost_lost'`, string(r.w.WID), shipmentID).Scan(&lost)
	if lost != -4500 {
		t.Errorf("shipping cost lost = %d, want -4500 (the courier's tariff)", lost)
	}
	// The expected parcel is on the task board.
	if n := r.count(`SELECT count(*) FROM tasks WHERE warehouse_id=$1 AND kind='return_intake' AND state='open'`, string(r.w.WID)); n != 1 {
		t.Errorf("open return_intake tasks = %d, want 1", n)
	}

	// The parcel arrives: 3 units, all good.
	rc, err := r.ret.Receive(r.ctx, r.w.WID, "olga", "TRK-LOOP", "intake-1", []returns.IntakeLine{{SKU: string(sku), Qty: 3, Condition: returns.Good}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Tasks) != 1 {
		t.Fatalf("disposition tasks = %v, want 1", rc.Tasks)
	}
	if got := r.stock(r.w.Returns, "available"); got != 3 {
		t.Errorf("units in the returns zone = %d, want 3", got)
	}
	// Restock them. The putaway rule picks the destination.
	if err := r.ret.Disposition(r.ctx, r.w.WID, rc.Tasks[0], returns.Restock, ""); err != nil {
		t.Fatal(err)
	}
	if got := r.stock(r.w.Pick, "available"); got != afterShip+3 {
		t.Errorf("pick face holds %d, want %d: the returned units are back on the shelf", got, afterShip+3)
	}

	// One timeline tells the whole story, in order.
	tl, err := r.svc.Timeline(r.ctx, r.w.WID, shipmentID)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ kind, contains string }{
		{"dispatched", "TRK-LOOP"},
		{"outcome", "rejected"},
		{"return_authorised", "cod rejected"},
		{"ledger", "Shipping cost lost: 45.00"},
		{"returned", "3 x " + string(sku)},
		{"dispositioned", "Restocked 3 x " + string(sku)},
	}
	var got []string
	for _, e := range tl {
		got = append(got, e.Kind+": "+e.Text)
	}
	pos := 0
	for _, w := range want {
		found := false
		for ; pos < len(tl); pos++ {
			if tl[pos].Kind == w.kind && strings.Contains(tl[pos].Text, w.contains) {
				found, pos = true, pos+1
				break
			}
		}
		if !found {
			t.Fatalf("timeline has no %q containing %q, in order:\n  %s", w.kind, w.contains, strings.Join(got, "\n  "))
		}
	}
	// And the return authorisation is finished.
	var state string
	_ = r.db.QueryRow(`SELECT state FROM return_authorisations WHERE warehouse_id=$1 AND id=$2`, string(r.w.WID), res.ReturnAuthorisationID).Scan(&state)
	if state != "dispositioned" {
		t.Errorf("return authorisation state = %s, want dispositioned", state)
	}
}

func TestOutcomeIsIdempotent(t *testing.T) {
	r := newRig(t)
	cod := int64(50000)
	r.shipRow("TRK-1", &cod)
	first := r.outcome("TRK-1", settlement.Rejected, r.clock)
	again := r.outcome("TRK-1", settlement.Rejected, r.clock.Add(time.Hour))
	if first.Duplicate || !again.Duplicate || again.OutcomeID != first.OutcomeID {
		t.Fatalf("first %+v again %+v", first, again)
	}
	if n := r.count(`SELECT count(*) FROM ledger WHERE warehouse_id=$1`, string(r.w.WID)); n != 1 {
		t.Errorf("ledger rows = %d after a repeated outcome, want 1", n)
	}
	if n := r.count(`SELECT count(*) FROM return_authorisations WHERE warehouse_id=$1`, string(r.w.WID)); n != 1 {
		t.Errorf("return authorisations = %d, want 1", n)
	}
	if n := r.count(`SELECT count(*) FROM outbox WHERE warehouse_id=$1 AND topic='shipment.delivery_outcome'`, string(r.w.WID)); n != 1 {
		t.Errorf("outcome events = %d, want 1", n)
	}
	if _, err := r.svc.RecordOutcome(r.ctx, r.w.WID, settlement.ShipmentRef{Tracking: "NOPE"}, settlement.Lost, r.clock, ""); err == nil {
		t.Error("an outcome for an unknown tracking number was accepted")
	}
	if _, err := r.svc.RecordOutcome(r.ctx, r.w.WID, settlement.ShipmentRef{Tracking: "TRK-1"}, "teleported", r.clock, ""); err == nil {
		t.Error("an unknown outcome was accepted")
	}
}

func TestDeliveredAndLostBookTheMoney(t *testing.T) {
	r := newRig(t)
	cod := int64(80000)
	r.shipRow("TRK-D", &cod)
	r.shipRow("TRK-L", &cod)
	r.outcome("TRK-D", settlement.DeliveredCollected, r.clock)
	r.outcome("TRK-L", settlement.Lost, r.clock)
	sm, err := r.svc.Summary(r.ctx, r.w.WID)
	if err != nil {
		t.Fatal(err)
	}
	if sm.CODDelivered != 80000 || sm.ClaimsOwed != 80000 || sm.Receivable != 80000 {
		t.Errorf("summary = %+v, want 80000 delivered, 80000 claim, 80000 receivable", sm)
	}
}

// ---------------------------------------------------------------------------
// Returns
// ---------------------------------------------------------------------------

func TestReturnIntakeAndDispositionRules(t *testing.T) {
	r := newRig(t)
	cod := int64(20000)
	sku := r.w.SKUs[1]
	_, _ = wmstest.ShipOrder(t, r.db, r.store, r.w, sku, 2, &cod, carrier, "TRK-R")
	r.outcome("TRK-R", settlement.Rejected, r.clock)

	// Nothing was expected for another parcel.
	if _, err := r.ret.Receive(r.ctx, r.w.WID, "olga", "TRK-OTHER", "k0", []returns.IntakeLine{{SKU: string(sku), Qty: 1, Condition: returns.Good}}); err == nil {
		t.Error("received a parcel nobody authorised")
	}
	// More than was shipped.
	if _, err := r.ret.Receive(r.ctx, r.w.WID, "olga", "TRK-R", "k1", []returns.IntakeLine{{SKU: string(sku), Qty: 3, Condition: returns.Good}}); err == nil {
		t.Error("received more than was shipped")
	}
	// One good unit, one damaged. A retried scan with the same key does nothing.
	lines := []returns.IntakeLine{{SKU: string(sku), Qty: 1, Condition: returns.Good}, {SKU: string(sku), Qty: 1, Condition: returns.Damaged}}
	rc, err := r.ret.Receive(r.ctx, r.w.WID, "olga", "TRK-R", "k2", lines)
	if err != nil || len(rc.Tasks) != 2 {
		t.Fatalf("receive = %+v, %v", rc, err)
	}
	again, err := r.ret.Receive(r.ctx, r.w.WID, "olga", "TRK-R", "k2", lines)
	if err != nil || !again.Duplicate {
		t.Fatalf("repeat receive = %+v, %v; want a no-op", again, err)
	}
	if n := r.count(`SELECT count(*) FROM movements WHERE warehouse_id=$1 AND kind='return_receive'`, string(r.w.WID)); n != 2 {
		t.Errorf("return_receive movements = %d, want 2", n)
	}
	if g, d := r.stock(r.w.Returns, "available"), r.stock(r.w.Returns, "damaged"); g != 1 || d != 1 {
		t.Errorf("returns zone holds %d good and %d damaged, want 1 and 1", g, d)
	}

	// The task carries a suggestion; the damaged unit cannot be restocked.
	var goodTask, badTask string
	for _, id := range rc.Tasks {
		tk, _ := tasks.Get(r.ctx, r.db, r.w.WID, id)
		if tk.Reason == returns.Restock {
			goodTask = id
		} else {
			badTask = id
			if tk.Reason != returns.Scrap {
				t.Errorf("damaged unit suggestion = %q, want scrap", tk.Reason)
			}
		}
	}
	if err := r.ret.Disposition(r.ctx, r.w.WID, badTask, returns.Restock, ""); err == nil {
		t.Error("restocked a damaged unit")
	}
	if err := r.ret.Disposition(r.ctx, r.w.WID, badTask, returns.Scrap, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.ret.Disposition(r.ctx, r.w.WID, badTask, returns.Scrap, ""); err != nil {
		t.Errorf("repeating a disposition should be a no-op: %v", err)
	}
	if err := r.ret.Disposition(r.ctx, r.w.WID, goodTask, returns.Quarantine, ""); err != nil {
		t.Fatal(err)
	}
	if got := r.stock(r.w.Quar, "quarantined"); got != 1 {
		t.Errorf("quarantine zone holds %d, want 1", got)
	}
	var scrapped int64
	_ = r.db.QueryRow(`SELECT COALESCE(SUM(damaged),0) FROM positions WHERE warehouse_id=$1 AND location='SCRAP'`, string(r.w.WID)).Scan(&scrapped)
	if scrapped != 1 {
		t.Errorf("SCRAP holds %d, want 1", scrapped)
	}
}

// ---------------------------------------------------------------------------
// Remittance matching: every break type from the generator (Phase 4 exit)
// ---------------------------------------------------------------------------

func TestEveryBreakTypeIsGeneratedAndCaught(t *testing.T) {
	r := newRig(t)
	var ships []settlement.GenShipment
	// 24 delivered COD shipments, 6 rejected, 2 lost.
	for i := 0; i < 24; i++ {
		cod := int64(30000 + i*2500)
		tr := fmt.Sprintf("DLV-%03d", i)
		r.shipRow(tr, &cod)
		r.outcome(tr, settlement.DeliveredCollected, r.clock.Add(-10*24*time.Hour)) // 10 days ago: past the 7 day window
		ships = append(ships, settlement.GenShipment{Tracking: tr, COD: cod, Outcome: settlement.DeliveredCollected})
	}
	for i := 0; i < 6; i++ {
		cod := int64(45000)
		tr := fmt.Sprintf("REJ-%03d", i)
		r.shipRow(tr, &cod)
		r.outcome(tr, settlement.Rejected, r.clock.Add(-9*24*time.Hour))
		ships = append(ships, settlement.GenShipment{Tracking: tr, COD: cod, Outcome: settlement.Rejected})
	}
	for i := 0; i < 2; i++ {
		cod := int64(60000)
		tr := fmt.Sprintf("LST-%03d", i)
		r.shipRow(tr, &cod)
		r.outcome(tr, settlement.Lost, r.clock.Add(-9*24*time.Hour))
		ships = append(ships, settlement.GenShipment{Tracking: tr, COD: cod, Outcome: settlement.Lost})
	}

	gen, err := settlement.GenerateRemittance(ships, settlement.GenConfig{
		Seed: 7, Terms: terms, Short: 4, Duplicate: 3, Unremitted: 5, RemittedForRejected: 2, Unknown: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.svc.Ingest(r.ctx, r.w.WID, carrier, "jnt-2026-10.csv", gen.CSV, "sue")
	if err != nil {
		t.Fatal(err)
	}
	// The four breaks that show on lines are caught at ingest.
	for _, kind := range []string{settlement.ShortRemitted, settlement.DuplicateRemittance, settlement.RemittedForRejected, settlement.UnknownShipment} {
		if res.Breaks[kind] != gen.Expected[kind] {
			t.Errorf("%s: matcher found %d, generator made %d", kind, res.Breaks[kind], gen.Expected[kind])
		}
	}
	if res.Matched != gen.Clean+3 { // the 3 duplicated shipments match once each
		t.Errorf("matched %d lines, want %d clean + 3 first copies of duplicates", res.Matched, gen.Clean)
	}
	// Unremitted shows once the courier's window has passed.
	opened, err := r.svc.FindUnremitted(r.ctx, r.w.WID)
	if err != nil || opened != gen.Expected[settlement.Unremitted] {
		t.Fatalf("FindUnremitted opened %d, %v; generator left out %d", opened, err, gen.Expected[settlement.Unremitted])
	}
	if again, _ := r.svc.FindUnremitted(r.ctx, r.w.WID); again != 0 {
		t.Errorf("a second scan opened %d more", again)
	}

	// Each case points at the tracking number the generator meant.
	cases, err := r.svc.ListBreaks(r.ctx, r.w.WID, "", "", time.Time{}, "", 200)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, n := range gen.Expected {
		total += n
	}
	if len(cases) != total {
		t.Fatalf("%d cases open, want %d", len(cases), total)
	}
	for _, c := range cases {
		if want := gen.ByTracking[c.Tracking]; want != c.Kind {
			t.Errorf("tracking %s: case is %s, generator intended %q", c.Tracking, c.Kind, want)
		}
	}
	// A short case carries the delta.
	for _, c := range cases {
		if c.Kind == settlement.ShortRemitted && (c.Delta <= 0 || c.Delta != c.Expected-c.Actual || c.RemittanceID == "") {
			t.Errorf("short case = %+v", c)
		}
	}
	// The books: cash booked for clean lines only.
	sm, _ := r.svc.Summary(r.ctx, r.w.WID)
	if sm.Remitted < gen.CleanNet {
		t.Errorf("remitted %d, want at least the clean lines' %d", sm.Remitted, gen.CleanNet)
	}

	// The same file again changes nothing.
	dup, err := r.svc.Ingest(r.ctx, r.w.WID, carrier, "renamed.csv", gen.CSV, "sue")
	if err != nil || !dup.Duplicate || dup.RemittanceID != res.RemittanceID {
		t.Fatalf("re-upload = %+v, %v", dup, err)
	}
	if n := r.count(`SELECT count(*) FROM settlement_breaks WHERE warehouse_id=$1`, string(r.w.WID)); n != total {
		t.Errorf("cases after a re-upload = %d, want %d", n, total)
	}
}

func TestLatePaymentClearsAnUnremittedCase(t *testing.T) {
	r := newRig(t)
	cod := int64(100000)
	r.shipRow("LATE-1", &cod)
	r.outcome("LATE-1", settlement.DeliveredCollected, r.clock.Add(-10*24*time.Hour))
	if n, _ := r.svc.FindUnremitted(r.ctx, r.w.WID); n != 1 {
		t.Fatalf("opened %d, want 1", n)
	}
	gen, _ := settlement.GenerateRemittance([]settlement.GenShipment{{Tracking: "LATE-1", COD: cod, Outcome: settlement.DeliveredCollected}},
		settlement.GenConfig{Seed: 1, Terms: terms})
	if _, err := r.svc.Ingest(r.ctx, r.w.WID, carrier, "late.csv", gen.CSV, "sue"); err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT count(*) FROM settlement_breaks WHERE warehouse_id=$1 AND state='open'`, string(r.w.WID)); n != 0 {
		t.Errorf("%d cases still open after the courier paid", n)
	}
	if n := r.count(`SELECT count(*) FROM settlement_breaks WHERE warehouse_id=$1 AND state='cleared'`, string(r.w.WID)); n != 1 {
		t.Errorf("cleared cases = %d, want 1", n)
	}
}

func TestInsideTheWindowIsNotYetUnremitted(t *testing.T) {
	r := newRig(t)
	cod := int64(100000)
	r.shipRow("SOON-1", &cod)
	r.outcome("SOON-1", settlement.DeliveredCollected, r.clock.Add(-3*24*time.Hour))
	if n, _ := r.svc.FindUnremitted(r.ctx, r.w.WID); n != 0 {
		t.Errorf("opened %d cases 3 days after delivery with a 7 day window", n)
	}
}

// ---------------------------------------------------------------------------
// Cases
// ---------------------------------------------------------------------------

func (r *rig) oneShortCase() settlement.Break {
	r.t.Helper()
	cod := int64(100000)
	r.shipRow("CASE-1", &cod)
	r.outcome("CASE-1", settlement.DeliveredCollected, r.clock.Add(-2*24*time.Hour))
	csv := "courier_ref,tracking,collected,fee,net\nCR-1,CASE-1,1000.00,200.00,800.00\n" // expected net 970.00
	if _, err := r.svc.Ingest(r.ctx, r.w.WID, carrier, "short.csv", []byte(csv), "sue"); err != nil {
		r.t.Fatal(err)
	}
	bs, _ := r.svc.ListBreaks(r.ctx, r.w.WID, "unclosed", "", time.Time{}, "", 10)
	if len(bs) != 1 || bs[0].Kind != settlement.ShortRemitted || bs[0].Delta != 17000 {
		r.t.Fatalf("cases = %+v, want one short_remitted of 170.00", bs)
	}
	return bs[0]
}

func TestResolvingACaseNeedsANoteAndAWriteoffNeedsAReason(t *testing.T) {
	r := newRig(t)
	c := r.oneShortCase()
	if err := r.svc.Resolve(r.ctx, r.w.WID, c.ID, settlement.ResWrittenOff, "  ", "data_error", "sue"); err == nil {
		t.Error("resolved without a note")
	}
	if err := r.svc.Resolve(r.ctx, r.w.WID, c.ID, settlement.ResWrittenOff, "courier will not pay", "", "sue"); err == nil {
		t.Error("wrote money off without a reason")
	}
	if err := r.svc.Resolve(r.ctx, r.w.WID, c.ID, settlement.ResWrittenOff, "courier will not pay", "because", "sue"); err == nil {
		t.Error("accepted an unclassified reason")
	}
	before, _ := r.svc.Summary(r.ctx, r.w.WID)
	if err := r.svc.Resolve(r.ctx, r.w.WID, c.ID, settlement.ResWrittenOff, "fee dispute lost, agreed with JNT account manager", "courier_fee_dispute", "sue"); err != nil {
		t.Fatal(err)
	}
	after, _ := r.svc.Summary(r.ctx, r.w.WID)
	if after.WrittenOff-before.WrittenOff != 17000 {
		t.Errorf("written off %d, want 17000", after.WrittenOff-before.WrittenOff)
	}
	got, _ := r.svc.GetBreak(r.ctx, r.w.WID, c.ID)
	if got.State != "resolved" || got.WriteoffReason != "courier_fee_dispute" || got.ResolvedBy != "sue" || got.ClosedAt == nil {
		t.Errorf("case = %+v", got)
	}
	// Same decision again: nothing happens. A different one is refused.
	if err := r.svc.Resolve(r.ctx, r.w.WID, c.ID, settlement.ResWrittenOff, "again", "courier_fee_dispute", "sue"); err != nil {
		t.Errorf("repeat: %v", err)
	}
	if err := r.svc.Resolve(r.ctx, r.w.WID, c.ID, settlement.ResRecovered, "changed my mind", "", "sue"); err == nil {
		t.Error("re-resolved a closed case differently")
	}
	if n := r.count(`SELECT count(*) FROM ledger WHERE warehouse_id=$1 AND kind='writeoff'`, string(r.w.WID)); n != 1 {
		t.Errorf("writeoff entries = %d, want 1", n)
	}
}

func TestRecoveredMoneyIsBooked(t *testing.T) {
	r := newRig(t)
	c := r.oneShortCase()
	if err := r.svc.Resolve(r.ctx, r.w.WID, c.ID, settlement.ResRecovered, "JNT paid the 170.00 balance on 14 Oct", "", "sue"); err != nil {
		t.Fatal(err)
	}
	sm, _ := r.svc.Summary(r.ctx, r.w.WID)
	if sm.Recovered != 17000 {
		t.Errorf("recovered %d, want 17000", sm.Recovered)
	}
}

func TestCasesAgeIntoBuckets(t *testing.T) {
	r := newRig(t)
	r.oneShortCase()
	r.clock = r.clock.Add(95 * 24 * time.Hour)
	ag, err := r.svc.Ageing(r.ctx, r.w.WID)
	if err != nil || ag.Over90 != 1 || ag.Total() != 1 {
		t.Fatalf("ageing = %+v, %v; want one case over 90 days", ag, err)
	}
}

// ---------------------------------------------------------------------------
// Files
// ---------------------------------------------------------------------------

func TestParseRemittanceNamesEveryBadRow(t *testing.T) {
	good := "courier_ref,tracking,collected,fee,net\nCR-1,T1,100.00,10.00,90.00\n"
	if ls, err := settlement.ParseRemittance([]byte(good)); err != nil || len(ls) != 1 || ls[0].Net != 9000 {
		t.Fatalf("good file: %+v, %v", ls, err)
	}
	// BOM, reordered columns, extra column, blank line.
	odd := "\ufeffNET,Tracking,collected,fee,courier_ref,note\n90.00,T1,100.00,10.00,CR-1,x\n\n"
	if ls, err := settlement.ParseRemittance([]byte(odd)); err != nil || len(ls) != 1 || ls[0].Tracking != "T1" {
		t.Fatalf("odd file: %+v, %v", ls, err)
	}
	cases := map[string]string{
		"missing column":   "courier_ref,tracking,collected,fee\nCR-1,T1,1.00,0.00\n",
		"bad amount":       "courier_ref,tracking,collected,fee,net\nCR-1,T1,1.00,0.00,0.50\nCR-2,T2,abc,0.00,1.00\n",
		"net over collected": "courier_ref,tracking,collected,fee,net\nCR-1,T1,1.00,0.50,0.80\n",
		"empty tracking":   "courier_ref,tracking,collected,fee,net\nCR-1,,1.00,0.00,1.00\n",
		"header only":      "courier_ref,tracking,collected,fee,net\n",
		"empty":            "",
	}
	for name, in := range cases {
		if _, err := settlement.ParseRemittance([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_, err := settlement.ParseRemittance([]byte(cases["bad amount"]))
	if err == nil || !strings.Contains(err.Error(), "row 2") {
		t.Errorf("error should name the bad row, got %v", err)
	}
}

func TestBadFileChangesNothing(t *testing.T) {
	r := newRig(t)
	cod := int64(100000)
	r.shipRow("BAD-1", &cod)
	bad := "courier_ref,tracking,collected,fee,net\nCR-1,BAD-1,1000.00,30.00,970.00\nCR-2,BAD-2,oops,0,0\n"
	if _, err := r.svc.Ingest(r.ctx, r.w.WID, carrier, "bad.csv", []byte(bad), "sue"); err == nil {
		t.Fatal("ingested a file with a bad row")
	}
	if n := r.count(`SELECT count(*) FROM remittances WHERE warehouse_id=$1`, string(r.w.WID)); n != 0 {
		t.Errorf("remittances stored = %d, want 0", n)
	}
	if n := r.count(`SELECT count(*) FROM ledger WHERE warehouse_id=$1`, string(r.w.WID)); n != 0 {
		t.Errorf("ledger rows = %d, want 0", n)
	}
}
