// Package wmstest holds shared helpers for integration tests that exercise the
// Phase 1 workflows against a real PostgreSQL: warehouse seeding and the
// invariant checks (spec 6.1) that every such test runs at the end.
package wmstest

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbound"
	"github.com/nicholasgasior/crossdock/internal/tasks"
)

// World is a small seeded warehouse.
type World struct {
	WID      inventory.WarehouseID
	Dock     inventory.LocationCode // receiving
	Pick     inventory.LocationCode // pick face
	Bulk     inventory.LocationCode // bulk
	Pack     inventory.LocationCode // packing station
	Returns  inventory.LocationCode // returns zone
	Quar     inventory.LocationCode // quarantine zone
	SKUs     []inventory.SKUID
	PickCap  int
	BulkCap  int
	zoneByID map[string]string
}

// Seed creates a warehouse with one location per zone purpose and n SKUs.
func Seed(t testing.TB, db *sql.DB, nSKU int) World {
	t.Helper()
	ctx := context.Background()
	w := World{
		WID:  inventory.WarehouseID(ids.New("wh")),
		Dock: "A00-00-A-1", Pick: "A01-01-A-1", Bulk: "C03-05-A-1", Pack: "P00-00-A-1", Returns: "R00-00-A-1", Quar: "Q00-00-A-1",
		PickCap: 100, BulkCap: 500,
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	exec(`INSERT INTO warehouses (id, name, time_zone) VALUES ($1,'Test','UTC')`, string(w.WID))
	zone := func(purpose string) string {
		id := fmt.Sprintf("zone-%s-%s", w.WID, purpose)
		exec(`INSERT INTO zones (id, warehouse_id, name, purpose) VALUES ($1,$2,$3,$4)`, id, string(w.WID), purpose, purpose)
		return id
	}
	loc := func(code inventory.LocationCode, zoneID string, max int) {
		exec(`INSERT INTO locations (code, warehouse_id, zone_id, max_units) VALUES ($1,$2,$3,$4)`, string(code), string(w.WID), zoneID, max)
	}
	loc(w.Dock, zone("receiving"), 1000)
	loc(w.Pick, zone("pick_face"), w.PickCap)
	loc(w.Bulk, zone("bulk"), w.BulkCap)
	loc(w.Pack, zone("packing"), 1000)
	loc(w.Returns, zone("returns"), 1000)
	loc(w.Quar, zone("quarantine"), 1000)
	for i := 0; i < nSKU; i++ {
		id := inventory.SKUID(fmt.Sprintf("SKU-%04d", i+1))
		exec(`INSERT INTO skus (id, warehouse_id, barcode, name, is_lot_tracked, velocity_class) VALUES ($1,$2,$3,$4,FALSE,'A')`,
			string(id), string(w.WID), fmt.Sprintf("49%011d", i+1), "Product "+string(id))
		w.SKUs = append(w.SKUs, id)
	}
	return w
}

// Stock puts qty of sku at loc as available stock using a receive movement,
// bypassing the inbound flow (for allocation tests that do not care about it).
func Stock(t testing.TB, db *sql.DB, store *inventory.Store, w World, sku inventory.SKUID, loc inventory.LocationCode, qty int64) inventory.LotID {
	t.Helper()
	lot := inventory.LotID("LOT-" + string(sku) + "-" + string(loc))
	if _, err := db.Exec(`INSERT INTO lots (id, sku_id, warehouse_id, received_at) VALUES ($1,$2,$3,NOW()) ON CONFLICT DO NOTHING`,
		string(lot), string(sku), string(w.WID)); err != nil {
		t.Fatalf("stock lot: %v", err)
	}
	err := store.AppendMovement(context.Background(), inventory.Movement{
		ID: ids.New("mvt"), WarehouseID: w.WID, Kind: inventory.KindReceive,
		FromLocation: inventory.VirtualSupplier, FromState: inventory.StateAvailable,
		ToLocation: loc, ToState: inventory.StateAvailable, SKUID: sku, LotID: lot, Qty: qty,
		OccurredAt: time.Now(), IdempotencyKey: ids.New("seed"),
	})
	if err != nil {
		t.Fatalf("stock: %v", err)
	}
	return lot
}

// CheckInvariants asserts invariants 1, 2, 3 and 7 for a warehouse. Call it at
// the end of every workflow test so a test that leaves the database
// inconsistent fails the build (spec 14.1).
func CheckInvariants(t testing.TB, db *sql.DB, store *inventory.Store, wid inventory.WarehouseID) {
	t.Helper()
	ctx := context.Background()

	var sum int64
	if err := db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(available+reserved+in_transit+quarantined+damaged),0) FROM positions WHERE warehouse_id=$1`,
		string(wid)).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum != 0 {
		t.Errorf("INVARIANT 1: conservation sum = %d, want 0", sum)
	}

	var bad int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM positions
		WHERE warehouse_id=$1 AND location NOT IN ('SUPPLIER','CUSTOMER','CARRIER','VARIANCE','SCRAP','RETURN_INTAKE')
		  AND (available<0 OR reserved<0 OR in_transit<0 OR quarantined<0 OR damaged<0)`, string(wid)).Scan(&bad); err != nil {
		t.Fatal(err)
	}
	if bad != 0 {
		t.Errorf("INVARIANT 2: %d real positions have a negative quantity", bad)
	}

	live, err := store.AllPositions(ctx, wid)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RebuildPositions(ctx, wid); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := store.AllPositions(ctx, wid)
	if err != nil {
		t.Fatal(err)
	}
	lm := map[inventory.PositionKey]inventory.Position{}
	for _, p := range live {
		lm[p.PositionKey] = p
	}
	// A rebuild may drop all-zero rows that the live table kept; compare non-zero rows.
	rm := map[inventory.PositionKey]inventory.Position{}
	for _, p := range rebuilt {
		rm[p.PositionKey] = p
	}
	for k, p := range lm {
		r := rm[k]
		if p.Available != r.Available || p.Reserved != r.Reserved || p.InTransit != r.InTransit ||
			p.Quarantined != r.Quarantined || p.Damaged != r.Damaged {
			t.Errorf("INVARIANT 3: %+v live=%+v rebuilt=%+v", k, p, r)
		}
	}
	for k := range rm {
		if _, ok := lm[k]; !ok {
			t.Errorf("INVARIANT 3: rebuilt has %+v which live lacks", k)
		}
	}

	CheckTaskCoupling(t, db, wid)
}

// TaskCouplingViolations counts invariant 7 violations: pick, putaway,
// count_adjust and disposition movements that lack a completed task, and
// completed pick, putaway and pack tasks that have no movement.
func TaskCouplingViolations(db *sql.DB, wid inventory.WarehouseID) (orphanMovements, emptyTasks int, err error) {
	ctx := context.Background()
	if err = db.QueryRowContext(ctx, `
		SELECT count(*) FROM movements m
		LEFT JOIN tasks t ON t.warehouse_id=m.warehouse_id AND t.id=m.task_id
		WHERE m.warehouse_id=$1 AND m.kind IN ('pick','putaway','count_adjust','disposition')
		  AND (t.id IS NULL OR t.state <> 'completed')`, string(wid)).Scan(&orphanMovements); err != nil {
		return
	}
	err = db.QueryRowContext(ctx, `
		SELECT count(*) FROM tasks t
		WHERE t.warehouse_id=$1 AND t.state='completed' AND t.kind IN ('pick','putaway','pack')
		  AND NOT EXISTS (SELECT 1 FROM movements m WHERE m.warehouse_id=t.warehouse_id AND m.task_id=t.id)`,
		string(wid)).Scan(&emptyTasks)
	return
}

// CheckTaskCoupling asserts invariant 7.
func CheckTaskCoupling(t testing.TB, db *sql.DB, wid inventory.WarehouseID) {
	t.Helper()
	o, e, err := TaskCouplingViolations(db, wid)
	if err != nil {
		t.Fatal(err)
	}
	if o != 0 || e != 0 {
		t.Errorf("INVARIANT 7: %d movements lack a completed task, %d completed tasks lack a movement", o, e)
	}
}

// AddPickLocation adds another pick-face location to the seeded warehouse.
func AddPickLocation(t testing.TB, db *sql.DB, w World, code inventory.LocationCode, maxUnits int) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO locations (code, warehouse_id, zone_id, max_units) VALUES ($1,$2,$3,$4)`,
		string(code), string(w.WID), fmt.Sprintf("zone-%s-pick_face", w.WID), maxUnits); err != nil {
		t.Fatalf("add location: %v", err)
	}
}

// ShipOrder runs a real order through allocation, wave, pick, pack and ship and
// returns the order and shipment ids. cod is the cash-on-delivery amount in minor
// units, or nil for a prepaid shipment.
func ShipOrder(t testing.TB, db *sql.DB, store *inventory.Store, w World, sku inventory.SKUID, qty int64, cod *int64, carrier, tracking string) (orderID, shipmentID string) {
	t.Helper()
	ctx := context.Background()
	Stock(t, db, store, w, sku, w.Pick, qty)
	out := outbound.NewService(db, store)
	o, err := out.CreateOrder(ctx, w.WID, "SO-"+tracking, []outbound.OrderLine{{SKU: sku, Qty: qty}})
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	if st, err := out.Allocate(ctx, w.WID, o.ID); err != nil || st != "allocated" {
		t.Fatalf("allocate = %q, %v", st, err)
	}
	if _, err := out.ReleaseWave(ctx, w.WID, []string{o.ID}); err != nil {
		t.Fatalf("wave: %v", err)
	}
	for {
		tk, err := tasks.Claim(ctx, db, w.WID, "shipper", []inventory.TaskKind{inventory.TaskPick, inventory.TaskPack})
		if err != nil {
			t.Fatal(err)
		}
		if tk == nil {
			break
		}
		if tk.Kind == inventory.TaskPick {
			err = out.CompletePick(ctx, w.WID, tk.ID)
		} else {
			err = out.CompletePack(ctx, w.WID, tk.ID)
		}
		if err != nil {
			t.Fatalf("%s: %v", tk.Kind, err)
		}
	}
	sh, err := out.Ship(ctx, w.WID, o.ID, carrier, tracking, cod)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return o.ID, sh.ID
}
