package outbound_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbound"
	"github.com/nicholasgasior/crossdock/internal/testdb"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

// TestChaosKilledConnectionsLeaveNoPartialReservation (spec 14.7): PostgreSQL
// backends are terminated while orders allocate and cancel. After the clients
// retry, every reservation either fully happened or did not: reserved stock
// equals the live allocations, and every invariant holds.
//
// The server is not restarted (that would take the shared test database down
// with it); terminating the backends mid-statement is what a crash looks like
// to the client, and PostgreSQL rolls back the open transaction the same way.
func TestChaosKilledConnectionsLeaveNoPartialReservation(t *testing.T) {
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	victim, kill := testdb.Chaos(t, db)
	victim.SetMaxOpenConns(6)
	out := outbound.NewService(victim, inventory.NewStore(victim))
	ctx := context.Background()

	w := wmstest.Seed(t, db, 3)
	for _, s := range w.SKUs {
		wmstest.Stock(t, db, store, w, s, w.Pick, 40)
	}
	const n = 40
	clean := outbound.NewService(db, store)
	orders := make([]string, n)
	for i := range orders {
		o, err := clean.CreateOrder(ctx, w.WID, fmt.Sprintf("SO-%d", i), []outbound.OrderLine{
			{SKU: w.SKUs[i%3], Qty: int64(1 + i%3)}, {SKU: w.SKUs[(i+1)%3], Qty: 1}})
		if err != nil {
			t.Fatal(err)
		}
		orders[i] = o.ID
	}

	stop := make(chan struct{})
	var killed int
	var killer sync.WaitGroup
	killer.Add(1)
	go func() {
		defer killer.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(120 * time.Millisecond):
				killed += kill()
			}
		}
	}()

	// retry repeats op until it succeeds or the domain refuses it. A killed
	// connection is an ordinary error here, so the client simply tries again.
	retry := func(op func() error) error {
		var err error
		for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
			if err = op(); err == nil || refused(err) {
				return nil
			}
		}
		return err
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for i, id := range orders {
		wg.Go(func() {
			errs <- retry(func() error { _, err := out.Allocate(ctx, w.WID, id); return err })
			if i%4 == 0 {
				errs <- retry(func() error { return out.Cancel(ctx, w.WID, id) })
			}
		})
	}
	wg.Wait()
	close(stop)
	killer.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("operation never succeeded: %v", err)
		}
	}
	t.Logf("terminated %d backends", killed)
	if killed == 0 {
		t.Fatal("no backend was terminated; the test proved nothing")
	}

	// All-or-nothing: what the positions say is reserved is exactly what live
	// allocations say, and each order line's counter matches its allocations.
	var reserved, allocated int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(reserved),0) FROM positions WHERE warehouse_id=$1 AND location=$2`,
		string(w.WID), string(w.Pick)).Scan(&reserved); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COALESCE(SUM(qty),0) FROM allocations WHERE warehouse_id=$1 AND state='reserved'`,
		string(w.WID)).Scan(&allocated); err != nil {
		t.Fatal(err)
	}
	if reserved != allocated {
		t.Errorf("positions reserve %d units but live allocations hold %d", reserved, allocated)
	}
	var drift int
	if err := db.QueryRow(`
		SELECT count(*) FROM order_lines l
		WHERE l.warehouse_id=$1 AND l.allocated_qty <> COALESCE(
		  (SELECT SUM(a.qty) FROM allocations a WHERE a.warehouse_id=l.warehouse_id AND a.order_line_id=l.id AND a.state <> 'released'),0)`,
		string(w.WID)).Scan(&drift); err != nil {
		t.Fatal(err)
	}
	if drift != 0 {
		t.Errorf("%d order lines disagree with their allocations", drift)
	}
	wmstest.CheckInvariants(t, db, store, w.WID)
}
