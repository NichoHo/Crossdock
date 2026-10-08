package outbound_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicholasgasior/crossdock/internal/inbound"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbound"
	"github.com/nicholasgasior/crossdock/internal/tasks"
	"github.com/nicholasgasior/crossdock/internal/testdb"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

// TestFullCycleInboundToShip drives ASN -> receive -> putaway -> order ->
// allocate -> wave -> pick -> pack -> ship and checks every invariant after.
func TestFullCycleInboundToShip(t *testing.T) {
	t.Parallel()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	in, out := inbound.NewService(db, store), outbound.NewService(db, store)
	ctx := context.Background()
	w := wmstest.Seed(t, db, 2)

	asn, err := in.CreateASN(ctx, w.WID, "PO-1", "Acme", []inbound.ASNLine{
		{SKU: w.SKUs[0], ExpectedQty: 10}, {SKU: w.SKUs[1], ExpectedQty: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	var putaways []string
	for i, l := range asn.Lines {
		r, err := in.Receive(ctx, inbound.ReceiveInput{
			WarehouseID: w.WID, ASNLineID: l.ID, Qty: l.ExpectedQty, LPN: inventory.LPNID(fmt.Sprintf("LPN-%d", i)),
			Location: w.Dock, Key: fmt.Sprintf("scan-%d", i),
		})
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if r.ToLocation != w.Pick {
			t.Errorf("putaway destination = %q, want pick face %q", r.ToLocation, w.Pick)
		}
		putaways = append(putaways, r.PutawayID)
	}
	for _, id := range putaways {
		if err := in.CompletePutaway(ctx, w.WID, id, ""); err != nil {
			t.Fatalf("putaway: %v", err)
		}
		if err := in.CompletePutaway(ctx, w.WID, id, ""); err != nil { // retry is a no-op
			t.Fatalf("putaway retry: %v", err)
		}
	}
	if got, _ := in.GetASN(ctx, w.WID, asn.ID); got.State != "received" {
		t.Errorf("asn state = %s, want received", got.State)
	}

	ord, err := out.CreateOrder(ctx, w.WID, "SO-1", []outbound.OrderLine{{SKU: w.SKUs[0], Qty: 4}, {SKU: w.SKUs[1], Qty: 5}})
	if err != nil {
		t.Fatal(err)
	}
	if st, err := out.Allocate(ctx, w.WID, ord.ID); err != nil || st != "allocated" {
		t.Fatalf("allocate = %q, %v", st, err)
	}
	wave, err := out.ReleaseWave(ctx, w.WID, []string{ord.ID})
	if err != nil || wave.PickTasks != 2 || wave.RouteDistance <= 0 {
		t.Fatalf("wave = %+v, %v", wave, err)
	}
	for {
		tk, err := tasks.Claim(ctx, db, w.WID, "op1", []inventory.TaskKind{inventory.TaskPick, inventory.TaskPack})
		if err != nil {
			t.Fatal(err)
		}
		if tk == nil {
			break
		}
		switch tk.Kind {
		case inventory.TaskPick:
			err = out.CompletePick(ctx, w.WID, tk.ID)
			if err == nil {
				err = out.CompletePick(ctx, w.WID, tk.ID) // retry
			}
		case inventory.TaskPack:
			err = out.CompletePack(ctx, w.WID, tk.ID)
		}
		if err != nil {
			t.Fatalf("%s: %v", tk.Kind, err)
		}
	}
	cod := int64(150000)
	sh, err := out.Ship(ctx, w.WID, ord.ID, "JNT", "TRK123", &cod)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := out.Ship(ctx, w.WID, ord.ID, "JNT", "TRK123", &cod); err != nil || again.ID != sh.ID {
		t.Fatalf("ship retry = %+v, %v", again, err)
	}
	if o, _ := out.GetOrder(ctx, w.WID, ord.ID); o.State != "shipped" {
		t.Errorf("order state = %s, want shipped", o.State)
	}

	// 9 units left the building: CARRIER holds -(-9)... carrier is +9, SUPPLIER -15, stock 6.
	var onHand int64
	_ = db.QueryRow(`SELECT COALESCE(SUM(available),0) FROM positions WHERE warehouse_id=$1 AND location=$2`,
		string(w.WID), string(w.Pick)).Scan(&onHand)
	if onHand != 6 {
		t.Errorf("pick face available = %d, want 6", onHand)
	}
	wmstest.CheckInvariants(t, db, store, w.WID)
}

// TestAllocationReservationBound: 50 orders race for one unit; exactly one wins
// (invariant 4 through the real allocation path).
func TestAllocationReservationBound(t *testing.T) {
	t.Parallel()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	out := outbound.NewService(db, store)
	ctx := context.Background()
	w := wmstest.Seed(t, db, 1)
	wmstest.Stock(t, db, store, w, w.SKUs[0], w.Pick, 1)

	const n = 50
	ids := make([]string, n)
	for i := range ids {
		o, err := out.CreateOrder(ctx, w.WID, fmt.Sprintf("SO-%d", i), []outbound.OrderLine{{SKU: w.SKUs[0], Qty: 1}})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = o.ID
	}
	var won, lost int64
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := out.Allocate(ctx, w.WID, id)
			if err != nil {
				t.Errorf("allocate: %v", err)
				return
			}
			if st == "allocated" {
				atomic.AddInt64(&won, 1)
			} else {
				atomic.AddInt64(&lost, 1)
			}
		}()
	}
	wg.Wait()
	if won != 1 || lost != n-1 {
		t.Fatalf("won=%d lost=%d, want 1 and %d", won, lost, n-1)
	}
	var reserves int
	_ = db.QueryRow(`SELECT count(*) FROM movements WHERE warehouse_id=$1 AND kind='reserve'`, string(w.WID)).Scan(&reserves)
	if reserves != 1 {
		t.Errorf("reserve movements = %d, want 1", reserves)
	}
	wmstest.CheckInvariants(t, db, store, w.WID)
}

// TestPartialAllocationAndSweep: a short-stocked order is partially allocated
// and completed by a later sweep once stock arrives.
func TestPartialAllocationAndSweep(t *testing.T) {
	t.Parallel()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	out := outbound.NewService(db, store)
	ctx := context.Background()
	w := wmstest.Seed(t, db, 1)
	wmstest.Stock(t, db, store, w, w.SKUs[0], w.Pick, 3)

	o, _ := out.CreateOrder(ctx, w.WID, "SO-P", []outbound.OrderLine{{SKU: w.SKUs[0], Qty: 5}})
	if st, err := out.Allocate(ctx, w.WID, o.ID); err != nil || st != "partially_allocated" {
		t.Fatalf("allocate = %q, %v", st, err)
	}
	if n, err := out.Sweep(ctx, w.WID); err != nil || n != 0 {
		t.Fatalf("sweep with no new stock changed %d, %v", n, err)
	}
	wmstest.Stock(t, db, store, w, w.SKUs[0], w.Bulk, 10)
	if n, err := out.Sweep(ctx, w.WID); err != nil || n != 1 {
		t.Fatalf("sweep after restock changed %d, %v", n, err)
	}
	got, _ := out.GetOrder(ctx, w.WID, o.ID)
	if got.State != "allocated" || got.Lines[0].AllocatedQty != 5 {
		t.Fatalf("order = %+v", got)
	}
	wmstest.CheckInvariants(t, db, store, w.WID)
}

// TestCancelReleasesExactlyOnce is invariant 5 through the real path: many
// concurrent and repeated cancels release the reservation once.
func TestCancelReleasesExactlyOnce(t *testing.T) {
	t.Parallel()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	out := outbound.NewService(db, store)
	ctx := context.Background()
	w := wmstest.Seed(t, db, 1)
	wmstest.Stock(t, db, store, w, w.SKUs[0], w.Pick, 10)

	o, _ := out.CreateOrder(ctx, w.WID, "SO-C", []outbound.OrderLine{{SKU: w.SKUs[0], Qty: 6}})
	if _, err := out.Allocate(ctx, w.WID, o.ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := out.Cancel(ctx, w.WID, o.ID); err != nil {
				t.Errorf("cancel: %v", err)
			}
		}()
	}
	wg.Wait()
	var avail, reserved int64
	_ = db.QueryRow(`SELECT available, reserved FROM positions WHERE warehouse_id=$1 AND location=$2`,
		string(w.WID), string(w.Pick)).Scan(&avail, &reserved)
	if avail != 10 || reserved != 0 {
		t.Fatalf("available=%d reserved=%d, want 10 and 0", avail, reserved)
	}
	var releases int
	_ = db.QueryRow(`SELECT count(*) FROM movements WHERE warehouse_id=$1 AND kind='release'`, string(w.WID)).Scan(&releases)
	if releases != 1 {
		t.Fatalf("release movements = %d, want 1", releases)
	}
	wmstest.CheckInvariants(t, db, store, w.WID)
}

// TestReceiveIsIdempotent: the same scan key twice makes one movement.
func TestReceiveIsIdempotent(t *testing.T) {
	t.Parallel()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	in := inbound.NewService(db, store)
	ctx := context.Background()
	w := wmstest.Seed(t, db, 1)
	asn, _ := in.CreateASN(ctx, w.WID, "PO-I", "Acme", []inbound.ASNLine{{SKU: w.SKUs[0], ExpectedQty: 10}})

	in1 := inbound.ReceiveInput{WarehouseID: w.WID, ASNLineID: asn.Lines[0].ID, Qty: 4, Location: w.Dock, Key: "k1"}
	r1, err := in.Receive(ctx, in1)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := in.Receive(ctx, in1)
	if err != nil || !r2.Duplicate || r2.ID != r1.ID {
		t.Fatalf("second receive = %+v, %v", r2, err)
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM movements WHERE warehouse_id=$1 AND kind='receive'`, string(w.WID)).Scan(&n)
	if n != 1 {
		t.Fatalf("receive movements = %d, want 1", n)
	}
}

// TestInvariant7_TaskMovementCoupling: movements made through the workflows
// always reference a completed task, and the checker catches a raw pick that
// bypasses the task system.
func TestInvariant7_TaskMovementCoupling(t *testing.T) {
	t.Parallel()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	w := wmstest.Seed(t, db, 1)
	lot := wmstest.Stock(t, db, store, w, w.SKUs[0], w.Pick, 5)
	if o, e, err := wmstest.TaskCouplingViolations(db, w.WID); err != nil || o != 0 || e != 0 {
		t.Fatalf("clean warehouse: %d, %d, %v", o, e, err)
	}
	for _, m := range []inventory.Movement{
		{ID: "raw-1", Kind: inventory.KindReserve, FromState: inventory.StateAvailable, ToState: inventory.StateReserved},
		{ID: "raw-2", Kind: inventory.KindPick, FromState: inventory.StateReserved, ToState: inventory.StateInTransit},
	} {
		m.WarehouseID, m.FromLocation, m.ToLocation, m.SKUID, m.LotID, m.Qty = w.WID, w.Pick, w.Pick, w.SKUs[0], lot, 1
		m.OccurredAt = time.Now()
		if err := store.AppendMovement(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	if o, _, _ := wmstest.TaskCouplingViolations(db, w.WID); o != 1 {
		t.Fatalf("expected the task-less pick to be flagged, got %d", o)
	}
}
