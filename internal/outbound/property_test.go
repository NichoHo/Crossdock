package outbound_test

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"sync"
	"testing"

	"pgregory.net/rapid"

	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbound"
	"github.com/nicholasgasior/crossdock/internal/tasks"
	"github.com/nicholasgasior/crossdock/internal/testdb"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

// Each check seeds a warehouse and rebuilds the projection, so rapid's default of
// 100 checks takes minutes against a real database. Default to 25; pass
// -rapid.checks=N for a deeper run.
func init() {
	if f := flag.Lookup("rapid.checks"); f != nil && f.Value.String() == f.DefValue {
		_ = f.Value.Set("25")
	}
}

// refused reports whether err is a domain refusal (the command was valid to
// send but the state did not allow it), as opposed to a failure.
func refused(err error) bool {
	for _, e := range []error{outbound.ErrNotFound, outbound.ErrBadState, outbound.ErrNotCancellable, apperr.ErrInvalid} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// conserved checks invariants 1 and 2 cheaply, so it can run after every step.
func conserved(db *sql.DB, wid inventory.WarehouseID) error {
	var sum int64
	var neg int
	if err := db.QueryRow(`
		SELECT COALESCE(SUM(available+reserved+in_transit+quarantined+damaged),0),
		       count(*) FILTER (WHERE location NOT IN ('SUPPLIER','CUSTOMER','CARRIER','VARIANCE','SCRAP','RETURN_INTAKE')
		         AND (available<0 OR reserved<0 OR in_transit<0 OR quarantined<0 OR damaged<0))
		FROM positions WHERE warehouse_id=$1`, string(wid)).Scan(&sum, &neg); err != nil {
		return err
	}
	if sum != 0 || neg != 0 {
		return fmt.Errorf("conservation sum %d, %d negative positions", sum, neg)
	}
	return nil
}

// TestPropertyRandomCommandsKeepInvariants (spec 14.3): any sequence of valid
// commands, through the real workflows, never breaks conservation or position
// identity, and the projection still equals a rebuild at the end.
func TestPropertyRandomCommandsKeepInvariants(t *testing.T) {
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	out := outbound.NewService(db, store)
	ctx := context.Background()

	rapid.Check(t, func(rt *rapid.T) {
		w := wmstest.Seed(t, db, 3)
		var orders []string
		sku := func() inventory.SKUID { return rapid.SampledFrom(w.SKUs).Draw(rt, "sku") }
		pick := func() string { return rapid.SampledFrom(orders).Draw(rt, "order") }

		steps := map[string]func(*rapid.T){
			"stock": func(rt *rapid.T) {
				loc := rapid.SampledFrom([]inventory.LocationCode{w.Pick, w.Bulk}).Draw(rt, "loc")
				wmstest.Stock(t, db, store, w, sku(), loc, rapid.Int64Range(1, 20).Draw(rt, "qty"))
			},
			"order": func(rt *rapid.T) {
				o, err := out.CreateOrder(ctx, w.WID, ids.New("so"), []outbound.OrderLine{{SKU: sku(), Qty: rapid.Int64Range(1, 10).Draw(rt, "qty")}})
				if err != nil {
					rt.Fatalf("create order: %v", err)
				}
				orders = append(orders, o.ID)
				if _, err := out.Allocate(ctx, w.WID, o.ID); err != nil && !refused(err) {
					rt.Fatalf("allocate: %v", err)
				}
			},
			"cancel": func(rt *rapid.T) {
				if len(orders) == 0 {
					rt.Skip()
				}
				if err := out.Cancel(ctx, w.WID, pick()); err != nil && !refused(err) {
					rt.Fatalf("cancel: %v", err)
				}
			},
			"sweep": func(rt *rapid.T) {
				if _, err := out.Sweep(ctx, w.WID); err != nil {
					rt.Fatalf("sweep: %v", err)
				}
			},
			"wave": func(rt *rapid.T) {
				if len(orders) == 0 {
					rt.Skip()
				}
				if _, err := out.ReleaseWave(ctx, w.WID, []string{pick()}); err != nil && !refused(err) {
					rt.Fatalf("wave: %v", err)
				}
			},
			"work": func(rt *rapid.T) {
				tk, err := tasks.Claim(ctx, db, w.WID, "prop", []inventory.TaskKind{inventory.TaskPick, inventory.TaskPack})
				if err != nil {
					rt.Fatalf("claim: %v", err)
				}
				if tk == nil {
					rt.Skip()
				}
				if tk.Kind == inventory.TaskPick {
					err = out.CompletePick(ctx, w.WID, tk.ID)
				} else {
					err = out.CompletePack(ctx, w.WID, tk.ID)
				}
				if err != nil && !refused(err) {
					rt.Fatalf("%s: %v", tk.Kind, err)
				}
			},
			"ship": func(rt *rapid.T) {
				if len(orders) == 0 {
					rt.Skip()
				}
				if _, err := out.Ship(ctx, w.WID, pick(), "JNT", ids.New("trk"), nil); err != nil && !refused(err) {
					rt.Fatalf("ship: %v", err)
				}
			},
			"": func(rt *rapid.T) { // runs after every step
				if err := conserved(db, w.WID); err != nil {
					rt.Fatal(err)
				}
			},
		}
		rt.Repeat(steps)
		wmstest.CheckInvariants(t, db, store, w.WID)
	})
}

// TestPropertyConcurrentReserveCancelMatchesSerial (spec 14.3): random
// interleavings of allocate and cancel, each retried, end with the same
// positions as running the same commands one at a time.
func TestPropertyConcurrentReserveCancelMatchesSerial(t *testing.T) {
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	out := outbound.NewService(db, store)
	ctx := context.Background()

	type cmd struct {
		sku     int
		qty     int64
		cancel  bool
		retries int
	}
	rapid.Check(t, func(rt *rapid.T) {
		cmds := rapid.SliceOfN(rapid.Custom(func(rt *rapid.T) cmd {
			return cmd{rapid.IntRange(0, 2).Draw(rt, "sku"), rapid.Int64Range(1, 5).Draw(rt, "qty"),
				rapid.Bool().Draw(rt, "cancel"), rapid.IntRange(1, 3).Draw(rt, "retries")}
		}), 1, 12).Draw(rt, "cmds")

		// Supply covers every order, so which order allocates first cannot change the outcome.
		setup := func() (wmstest.World, []string) {
			w := wmstest.Seed(t, db, 3)
			for _, s := range w.SKUs {
				wmstest.Stock(t, db, store, w, s, w.Pick, 60)
			}
			ids := make([]string, len(cmds))
			for i, c := range cmds {
				o, err := out.CreateOrder(ctx, w.WID, fmt.Sprintf("SO-%d", i), []outbound.OrderLine{{SKU: w.SKUs[c.sku], Qty: c.qty}})
				if err != nil {
					rt.Fatalf("create order: %v", err)
				}
				ids[i] = o.ID
			}
			return w, ids
		}
		run := func(w wmstest.World, id string, cancel bool, n int) error {
			for range n {
				var err error
				if cancel {
					err = out.Cancel(ctx, w.WID, id)
				} else {
					_, err = out.Allocate(ctx, w.WID, id)
				}
				if err != nil && !refused(err) {
					return err
				}
			}
			return nil
		}

		serial, sIDs := setup()
		for i, c := range cmds {
			if err := run(serial, sIDs[i], false, c.retries); err != nil {
				rt.Fatal(err)
			}
			if c.cancel {
				if err := run(serial, sIDs[i], true, c.retries); err != nil {
					rt.Fatal(err)
				}
			}
		}

		// Concurrent: every allocate and every cancel in its own goroutine, so a
		// cancel can land before, during or after its order's allocation.
		conc, cIDs := setup()
		var wg sync.WaitGroup
		errs := make(chan error, 2*len(cmds))
		for i, c := range cmds {
			wg.Go(func() { errs <- run(conc, cIDs[i], false, c.retries) })
			if c.cancel {
				wg.Go(func() { errs <- run(conc, cIDs[i], true, c.retries) })
			}
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				rt.Fatal(err)
			}
		}

		// A cancel that won the race leaves its order unallocated, as the serial
		// run does, so the positions must match exactly.
		if s, c := positions(rt, store, serial), positions(rt, store, conc); s != c {
			rt.Fatalf("positions differ\nserial:     %s\nconcurrent: %s", s, c)
		}
		wmstest.CheckInvariants(t, db, store, conc.WID)
	})
}

// positions renders a warehouse's non-zero positions without the warehouse id,
// so two warehouses can be compared.
func positions(rt *rapid.T, store *inventory.Store, w wmstest.World) string {
	ps, err := store.AllPositions(context.Background(), w.WID)
	if err != nil {
		rt.Fatal(err)
	}
	m := map[string]string{}
	for _, p := range ps {
		if p.Available|p.Reserved|p.InTransit|p.Quarantined|p.Damaged == 0 {
			continue
		}
		m[fmt.Sprintf("%s/%s/%s", p.Location, p.SKU, p.LPN)] = fmt.Sprintf("a%d r%d t%d q%d d%d", p.Available, p.Reserved, p.InTransit, p.Quarantined, p.Damaged)
	}
	return fmt.Sprint(m) // fmt sorts map keys
}
