package reconcile

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/nicholasgasior/crossdock/internal/inbound"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/testdb"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

// evaluate is the rule set. Each break kind is a row.
func TestEvaluate(t *testing.T) {
	now := time.Now()
	old, fresh := now.Add(-3*time.Hour), now.Add(-10*time.Minute)
	grace := 2 * time.Hour
	cases := []struct {
		name string
		f    lineFacts
		want []string
	}{
		{"clean", lineFacts{expected: 10, received: 10, putaway: 10, oldestReceipt: &old}, nil},
		{"open ASN, partly received, not yet a break", lineFacts{expected: 10, received: 4, putaway: 4, oldestReceipt: &old}, nil},
		{"closed short", lineFacts{expected: 10, received: 6, putaway: 6, short: true, oldestReceipt: &old}, []string{ShortShipped}},
		{"over shipped", lineFacts{expected: 5, received: 8, putaway: 8, oldestReceipt: &old}, []string{OverShipped}},
		{"putaway waiting too long", lineFacts{expected: 5, received: 5, putaway: 2, oldestReceipt: &old}, []string{PutawayIncomplete}},
		{"putaway still within grace", lineFacts{expected: 5, received: 5, putaway: 2, oldestReceipt: &fresh}, nil},
		{"putaway in exception is a break at once", lineFacts{expected: 5, received: 5, putaway: 0, oldestReceipt: &fresh, putawayException: true}, []string{PutawayIncomplete}},
		{"wrong lot", lineFacts{expected: 5, received: 5, putaway: 5, expectedLot: "LOT-A", badLots: 1, oldestReceipt: &old}, []string{LotMismatch}},
		{"short and over cannot both hold, but short plus unfinished putaway can", lineFacts{expected: 10, received: 6, putaway: 3, short: true, oldestReceipt: &old}, []string{ShortShipped, PutawayIncomplete}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, b := range evaluate(c.f, now, grace) {
				got = append(got, b.kind)
			}
			if len(got) != len(c.want) {
				t.Fatalf("breaks = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("breaks = %v, want %v", got, c.want)
				}
			}
		})
	}
}

type rig struct {
	t     *testing.T
	ctx   context.Context
	db    *sql.DB
	w     wmstest.World
	in    *inbound.Service
	svc   *Service
	clock time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	r := &rig{t: t, ctx: context.Background(), db: db, w: wmstest.Seed(t, db, 2), in: inbound.NewService(db, store), svc: NewService(db), clock: time.Now()}
	r.svc.SetClock(func() time.Time { return r.clock })
	t.Cleanup(func() { wmstest.CheckInvariants(t, db, store, r.w.WID) })
	return r
}

// receive creates an ASN with one line and receives qty against it.
func (r *rig) receive(ref string, expected, qty int64, expectedLot, actualLot string) (asn inbound.ASN, receipt inbound.Receipt) {
	r.t.Helper()
	asn, err := r.in.CreateASN(r.ctx, r.w.WID, ref, "Harbour Foods", []inbound.ASNLine{{SKU: r.w.SKUs[0], Lot: inventory.LotID(expectedLot), ExpectedQty: expected}})
	if err != nil {
		r.t.Fatal(err)
	}
	if qty > 0 {
		receipt, err = r.in.Receive(r.ctx, inbound.ReceiveInput{WarehouseID: r.w.WID, ASNLineID: asn.Lines[0].ID, Qty: qty,
			Location: r.w.Dock, Lot: inventory.LotID(actualLot), Key: "k-" + ref})
		if err != nil {
			r.t.Fatal(err)
		}
	}
	return asn, receipt
}

func (r *rig) run() (int, int) {
	r.t.Helper()
	o, c, err := r.svc.Run(r.ctx, r.w.WID)
	if err != nil {
		r.t.Fatal(err)
	}
	return o, c
}

func (r *rig) breaks() map[string]Break {
	r.t.Helper()
	bs, err := r.svc.List(r.ctx, r.w.WID, "", "", time.Time{}, "", 100)
	if err != nil {
		r.t.Fatal(err)
	}
	m := map[string]Break{}
	for _, b := range bs {
		m[b.Kind] = b
	}
	return m
}

func TestShortShippedCarriesSupplierAndASNReference(t *testing.T) {
	r := newRig(t)
	asn, rc := r.receive("PO-100", 10, 6, "", "")
	if err := r.in.CompletePutaway(r.ctx, r.w.WID, rc.PutawayID, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.in.MarkShort(r.ctx, r.w.WID, asn.Lines[0].ID); err != nil {
		t.Fatal(err)
	}
	if o, _ := r.run(); o != 1 {
		t.Fatalf("opened %d breaks, want 1", o)
	}
	b := r.breaks()[ShortShipped]
	if b.ASNRef != "PO-100" || b.Supplier != "Harbour Foods" || b.Expected != 10 || b.Received != 6 || b.Putaway != 6 || b.State != "open" {
		t.Fatalf("break = %+v", b)
	}
	// Running again changes nothing.
	if o, c := r.run(); o != 0 || c != 0 {
		t.Errorf("second run opened %d cleared %d", o, c)
	}
	// Waiving needs a note, and a waived break stays waived.
	if err := r.svc.Waive(r.ctx, r.w.WID, b.ID, "  ", "sue"); err == nil {
		t.Error("waived without a note")
	}
	if err := r.svc.Waive(r.ctx, r.w.WID, b.ID, "credit note CN-77 agreed", "sue"); err != nil {
		t.Fatal(err)
	}
	r.run()
	if got := r.breaks()[ShortShipped]; got.State != "waived" || got.Note == "" || got.ClosedBy != "sue" {
		t.Errorf("after waive: %+v", got)
	}
}

func TestOverShipped(t *testing.T) {
	r := newRig(t)
	_, rc := r.receive("PO-200", 5, 8, "", "")
	_ = r.in.CompletePutaway(r.ctx, r.w.WID, rc.PutawayID, "")
	r.run()
	if b, ok := r.breaks()[OverShipped]; !ok || b.Received != 8 || b.Expected != 5 {
		t.Fatalf("breaks = %+v", r.breaks())
	}
}

func TestPutawayIncompleteOpensAfterGraceAndClearsOnItsOwn(t *testing.T) {
	r := newRig(t)
	_, rc := r.receive("PO-300", 5, 5, "", "")
	if o, _ := r.run(); o != 0 {
		t.Fatalf("a receipt 0 minutes old opened %d breaks", o)
	}
	r.clock = r.clock.Add(3 * time.Hour)
	if o, _ := r.run(); o != 1 {
		t.Fatalf("after the grace period opened %d, want 1", o)
	}
	if b := r.breaks()[PutawayIncomplete]; b.Received != 5 || b.Putaway != 0 {
		t.Fatalf("break = %+v", b)
	}
	// The putaway finally happens: the break clears without anyone touching it.
	if err := r.in.CompletePutaway(r.ctx, r.w.WID, rc.PutawayID, ""); err != nil {
		t.Fatal(err)
	}
	if _, c := r.run(); c != 1 {
		t.Fatalf("cleared %d, want 1", c)
	}
	if b := r.breaks()[PutawayIncomplete]; b.State != "cleared" || b.ClosedAt == nil {
		t.Errorf("break = %+v, want cleared", b)
	}
}

func TestLotMismatch(t *testing.T) {
	r := newRig(t)
	_, rc := r.receive("PO-400", 5, 5, "LOT-A", "LOT-B")
	_ = r.in.CompletePutaway(r.ctx, r.w.WID, rc.PutawayID, "")
	r.run()
	if b, ok := r.breaks()[LotMismatch]; !ok || b.ASNRef != "PO-400" {
		t.Fatalf("breaks = %+v", r.breaks())
	}
}

func TestCleanReceiptHasNoBreaks(t *testing.T) {
	r := newRig(t)
	_, rc := r.receive("PO-500", 5, 5, "", "")
	_ = r.in.CompletePutaway(r.ctx, r.w.WID, rc.PutawayID, "")
	r.clock = r.clock.Add(5 * time.Hour)
	if o, c := r.run(); o != 0 || c != 0 || len(r.breaks()) != 0 {
		t.Fatalf("opened %d cleared %d breaks %+v", o, c, r.breaks())
	}
}

func TestBreaksAgeIntoBuckets(t *testing.T) {
	r := newRig(t)
	r.receive("PO-600", 5, 8, "", "") // over shipped, putaway pending
	r.run()
	r.clock = r.clock.Add(40 * 24 * time.Hour)
	ag, err := r.svc.Ageing(r.ctx, r.w.WID)
	if err != nil {
		t.Fatal(err)
	}
	if ag.D30to90 != 1 || ag.Total() != 1 {
		t.Fatalf("ageing = %+v, want one break in 30 to 90 days", ag)
	}
}
