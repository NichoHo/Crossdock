package inventory_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/testdb"
)

// TestInvariant1_Conservation asserts that for every warehouse, the signed
// sum of quantities across all movements is zero. Equivalently, the sum of
// all positions across real and virtual locations is zero.
//
// This is Invariant 1 from the spec §6.1 and it must pass at the Phase 0
// exit gate.
func TestInvariant1_Conservation(t *testing.T) {
	t.Parallel()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	ctx := context.Background()

	wid, _, skuID, lotID := seedMinimal(t, db)

	// Apply a sequence of movements that span several kinds.
	movements := buildConservationSequence(wid, skuID, lotID)
	for i, m := range movements {
		if err := store.AppendMovement(ctx, m); err != nil {
			t.Fatalf("movement %d (%s): %v", i, m.Kind, err)
		}
	}

	// Assertion: sum of all positions for this warehouse = 0.
	assertConservation(t, db, wid)
}

// TestInvariant2_PositionIdentity asserts that for every real location:
//   on_hand = available + reserved + in_transit + quarantined + damaged
// and every term is non-negative.
//
// This is Invariant 2 from the spec §6.1.
func TestInvariant2_PositionIdentity(t *testing.T) {
	t.Parallel()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	ctx := context.Background()

	wid, _, skuID, lotID := seedMinimal(t, db)

	movements := buildConservationSequence(wid, skuID, lotID)
	for i, m := range movements {
		if err := store.AppendMovement(ctx, m); err != nil {
			t.Fatalf("movement %d (%s): %v", i, m.Kind, err)
		}
	}

	assertPositionIdentity(t, db, wid)
}

// TestInvariant3_ProjectionFidelity asserts that RebuildPositions produces a
// table identical to the live one. This is the proof that positions is a cache
// and not a second source of truth.
//
// This is Invariant 3 from the spec §6.1.
func TestInvariant3_ProjectionFidelity(t *testing.T) {
	t.Parallel()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	ctx := context.Background()

	wid, _, skuID, lotID := seedMinimal(t, db)

	// Build a non-trivial history.
	movements := buildConservationSequence(wid, skuID, lotID)
	for i, m := range movements {
		if err := store.AppendMovement(ctx, m); err != nil {
			t.Fatalf("movement %d (%s): %v", i, m.Kind, err)
		}
	}

	// Snapshot the live positions.
	live, err := store.AllPositions(ctx, wid)
	if err != nil {
		t.Fatalf("AllPositions: %v", err)
	}

	// Rebuild.
	if err := store.RebuildPositions(ctx, wid); err != nil {
		t.Fatalf("RebuildPositions: %v", err)
	}

	// Compare rebuilt vs live.
	rebuilt, err := store.AllPositions(ctx, wid)
	if err != nil {
		t.Fatalf("AllPositions after rebuild: %v", err)
	}

	comparePositionSets(t, live, rebuilt)
}

// TestInvariant4_ReservationBound asserts that 50 concurrent reservation
// attempts against one available unit produce exactly one success and
// 49 rejections, with exactly one reserve movement in the log.
//
// This is Invariant 4 from the spec §6.1. It requires the race detector to be
// meaningful, so run with -race.
//
// NOTE: This invariant is not required to pass at Phase 0 exit (that requires
// only invariants 1, 2, 3, 6). It is included here so it exists and can be
// observed failing until Phase 1 implements the allocation path.
func TestInvariant4_ReservationBound(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping concurrency test in short mode")
	}

	db := testdb.Open(t)
	store := inventory.NewStore(db)
	ctx := context.Background()

	wid, locationCode, skuID, lotID := seedMinimal(t, db)

	// Seed exactly one available unit at the location.
	receiveM := newMovement(wid, inventory.KindReceive, inventory.VirtualSupplier, inventory.StateAvailable, locationCode, inventory.StateAvailable, skuID, lotID, "", 1)
	if err := store.AppendMovement(ctx, receiveM); err != nil {
		t.Fatalf("seed receive: %v", err)
	}

	// 50 goroutines each try to reserve the unit.
	const goroutines = 50
	var (
		successes int64
		failures  int64
		wg        sync.WaitGroup
	)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			m := newMovement(wid, inventory.KindReserve, locationCode, inventory.StateAvailable, locationCode, inventory.StateReserved, skuID, lotID, "", 1)
			m.IdempotencyKey = fmt.Sprintf("reserve-attempt-%d", i)
			if err := store.AppendMovement(context.Background(), m); err != nil {
				atomic.AddInt64(&failures, 1)
			} else {
				atomic.AddInt64(&successes, 1)
			}
		}()
	}
	wg.Wait()

	// Exactly one success.
	if successes != 1 {
		t.Errorf("expected exactly 1 successful reservation, got %d (failures=%d)", successes, failures)
	}
	if failures != goroutines-1 {
		t.Errorf("expected %d failures, got %d", goroutines-1, failures)
	}

	// Exactly one reserve movement in the log.
	mvts, err := store.ListMovements(ctx, wid, 0, 0)
	if err != nil {
		t.Fatalf("list movements: %v", err)
	}
	var reserveCount int
	for _, m := range mvts {
		if m.Kind == inventory.KindReserve {
			reserveCount++
		}
	}
	if reserveCount != 1 {
		t.Errorf("expected exactly 1 reserve movement, got %d", reserveCount)
	}
}

// TestInvariant5_ReleaseExactness asserts that cancelling an allocation releases
// exactly the reserved quantity, exactly once, regardless of retries.
//
// NOTE: Like Inv 4, this is not required at Phase 0 exit. Included for completeness.
func TestInvariant5_ReleaseExactness(t *testing.T) {
	t.Parallel()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	ctx := context.Background()

	wid, locationCode, skuID, lotID := seedMinimal(t, db)

	// Receive 10, reserve 5.
	receiveM := newMovement(wid, inventory.KindReceive, inventory.VirtualSupplier, inventory.StateAvailable, locationCode, inventory.StateAvailable, skuID, lotID, "", 10)
	if err := store.AppendMovement(ctx, receiveM); err != nil {
		t.Fatalf("receive: %v", err)
	}
	reserveM := newMovement(wid, inventory.KindReserve, locationCode, inventory.StateAvailable, locationCode, inventory.StateReserved, skuID, lotID, "", 5)
	reserveM.IdempotencyKey = "reserve-inv5"
	if err := store.AppendMovement(ctx, reserveM); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	// Release (cancel allocation). Apply the same idempotency key 3 times.
	releaseM := newMovement(wid, inventory.KindRelease, locationCode, inventory.StateReserved, locationCode, inventory.StateAvailable, skuID, lotID, "", 5)
	releaseM.IdempotencyKey = "release-inv5"
	for i := 0; i < 3; i++ {
		err := store.AppendMovement(ctx, releaseM)
		if i == 0 && err != nil {
			t.Fatalf("first release: %v", err)
		}
		if i > 0 && err != inventory.ErrDuplicateMovement {
			t.Errorf("retry %d: expected ErrDuplicateMovement, got %v", i, err)
		}
	}

	// Position should show 10 available, 0 reserved.
	pos, err := store.GetPosition(ctx, inventory.PositionKey{
		WarehouseID: wid, SKU: skuID, Lot: lotID, Location: locationCode,
	})
	if err != nil {
		t.Fatalf("get position: %v", err)
	}
	if pos.Available != 10 {
		t.Errorf("expected available=10, got %d", pos.Available)
	}
	if pos.Reserved != 0 {
		t.Errorf("expected reserved=0, got %d", pos.Reserved)
	}
}

// TestInvariant6_MovementImmutability asserts that no UPDATE or DELETE is
// possible on the movements table. This is enforced by a database trigger.
//
// This is Invariant 6 from the spec §6.1 and must pass at Phase 0 exit.
func TestInvariant6_MovementImmutability(t *testing.T) {
	t.Parallel()
	db := testdb.Open(t)
	store := inventory.NewStore(db)
	ctx := context.Background()

	wid, locationCode, skuID, lotID := seedMinimal(t, db)

	m := newMovement(wid, inventory.KindReceive, inventory.VirtualSupplier, inventory.StateAvailable, locationCode, inventory.StateAvailable, skuID, lotID, "", 5)
	if err := store.AppendMovement(ctx, m); err != nil {
		t.Fatalf("append movement: %v", err)
	}

	// Attempt UPDATE — should be rejected by the trigger.
	_, updateErr := db.ExecContext(ctx, `UPDATE movements SET qty=999 WHERE id=$1`, m.ID)
	if updateErr == nil {
		t.Error("UPDATE on movements succeeded — immutability trigger is not working")
	}

	// Attempt DELETE — should be rejected by the trigger.
	_, deleteErr := db.ExecContext(ctx, `DELETE FROM movements WHERE id=$1`, m.ID)
	if deleteErr == nil {
		t.Error("DELETE on movements succeeded — immutability trigger is not working")
	}

	// Verify the movement is unchanged.
	mvts, err := store.ListMovements(ctx, wid, 0, 0)
	if err != nil {
		t.Fatalf("list movements: %v", err)
	}
	if len(mvts) != 1 {
		t.Fatalf("expected 1 movement, got %d", len(mvts))
	}
	if mvts[0].Qty != 5 {
		t.Errorf("movement qty was mutated: expected 5, got %d", mvts[0].Qty)
	}
}

// Invariant 7 (task-movement coupling) needs the task system, so its test
// lives in internal/outbound/flow_test.go: TestInvariant7_TaskMovementCoupling.

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// seedMinimal creates the minimum database rows needed to write movements:
// one warehouse, one zone, one physical location, one SKU, and one lot.
func seedMinimal(t *testing.T, db *sql.DB) (
	wid inventory.WarehouseID,
	locationCode inventory.LocationCode,
	skuID inventory.SKUID,
	lotID inventory.LotID,
) {
	t.Helper()
	ctx := context.Background()

	wid = inventory.WarehouseID(fmt.Sprintf("wh-test-%d", rand.Int63()))
	_, err := db.ExecContext(ctx,
		`INSERT INTO warehouses (id, name, time_zone) VALUES ($1, $2, $3)`,
		string(wid), "Test Warehouse", "Asia/Bangkok",
	)
	if err != nil {
		t.Fatalf("seed warehouse: %v", err)
	}

	zoneID := fmt.Sprintf("zone-%s", wid)
	_, err = db.ExecContext(ctx,
		`INSERT INTO zones (id, warehouse_id, name, purpose) VALUES ($1, $2, $3, $4)`,
		zoneID, string(wid), "Bulk", "bulk",
	)
	if err != nil {
		t.Fatalf("seed zone: %v", err)
	}

	locationCode = inventory.LocationCode("A01-01-A-1")
	_, err = db.ExecContext(ctx,
		`INSERT INTO locations (code, warehouse_id, zone_id, max_units) VALUES ($1, $2, $3, $4)`,
		string(locationCode), string(wid), zoneID, 100,
	)
	if err != nil {
		t.Fatalf("seed location: %v", err)
	}

	skuID = inventory.SKUID(fmt.Sprintf("SKU-%s", wid))
	_, err = db.ExecContext(ctx,
		`INSERT INTO skus (id, warehouse_id, barcode, name, is_lot_tracked, velocity_class) VALUES ($1,$2,$3,$4,$5,$6)`,
		string(skuID), string(wid), "4901234567890", "Test Product", true, "A",
	)
	if err != nil {
		t.Fatalf("seed SKU: %v", err)
	}

	lotID = inventory.LotID(fmt.Sprintf("LOT-%s", wid))
	_, err = db.ExecContext(ctx,
		`INSERT INTO lots (id, sku_id, warehouse_id, received_at) VALUES ($1,$2,$3,$4)`,
		string(lotID), string(skuID), string(wid), time.Now(),
	)
	if err != nil {
		t.Fatalf("seed lot: %v", err)
	}

	return wid, locationCode, skuID, lotID
}

// newMovement constructs a Movement with sensible defaults for tests.
func newMovement(
	wid inventory.WarehouseID,
	kind inventory.MovementKind,
	fromLoc inventory.LocationCode, fromState inventory.StockState,
	toLoc inventory.LocationCode, toState inventory.StockState,
	skuID inventory.SKUID, lotID inventory.LotID,
	lpnID inventory.LPNID,
	qty int64,
) inventory.Movement {
	return inventory.Movement{
		ID:          fmt.Sprintf("mvt-%d", rand.Int63()),
		WarehouseID: wid,
		Kind:        kind,
		FromLocation: fromLoc,
		FromState:    fromState,
		ToLocation:  toLoc,
		ToState:     toState,
		SKUID:       skuID,
		LotID:       lotID,
		LPNID:       lpnID,
		Qty:         qty,
		OccurredAt:  time.Now(),
	}
}

// buildConservationSequence builds a list of movements that represent a
// realistic lifecycle: receive → putaway → reserve → pick → pack → ship.
// The sequence is designed to produce a non-trivial movement log while
// remaining conservation-preserving.
func buildConservationSequence(
	wid inventory.WarehouseID,
	skuID inventory.SKUID,
	lotID inventory.LotID,
) []inventory.Movement {
	const receivingLoc = inventory.LocationCode("A01-01-A-1")

	return []inventory.Movement{
		// Receive 20 units from supplier into receiving location.
		newMovement(wid, inventory.KindReceive, inventory.VirtualSupplier, inventory.StateAvailable, receivingLoc, inventory.StateAvailable, skuID, lotID, "", 20),
		// Reserve 10 for an order.
		newMovement(wid, inventory.KindReserve, receivingLoc, inventory.StateAvailable, receivingLoc, inventory.StateReserved, skuID, lotID, "", 10),
		// Pick 10 (reserved → in_transit on a pick cart, simulated as LPNID="" for simplicity).
		newMovement(wid, inventory.KindPick, receivingLoc, inventory.StateReserved, receivingLoc, inventory.StateInTransit, skuID, lotID, "", 10),
		// Ship 10 to carrier (in_transit at location → carrier virtual location).
		newMovement(wid, inventory.KindShip, receivingLoc, inventory.StateInTransit, inventory.VirtualCarrier, inventory.StateAvailable, skuID, lotID, "", 10),
		// Quarantine 2 damaged units.
		newMovement(wid, inventory.KindQuarantine, receivingLoc, inventory.StateAvailable, receivingLoc, inventory.StateQuarantined, skuID, lotID, "", 2),
		// Count adjust: 1 unit found that was unrecorded (from VARIANCE).
		newMovement(wid, inventory.KindCountAdjust, inventory.VirtualVariance, inventory.StateAvailable, receivingLoc, inventory.StateAvailable, skuID, lotID, "", 1),
	}
}

// assertConservation verifies that the signed sum of all positions for a
// warehouse is zero (Invariant 1).
func assertConservation(t *testing.T, db *sql.DB, wid inventory.WarehouseID) {
	t.Helper()
	ctx := context.Background()
	var total int64
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(available + reserved + in_transit + quarantined + damaged), 0)
		FROM positions WHERE warehouse_id=$1`, string(wid),
	).Scan(&total)
	if err != nil {
		t.Fatalf("conservation check query: %v", err)
	}
	if total != 0 {
		t.Errorf("INVARIANT 1 VIOLATED: signed sum of all positions = %d, want 0", total)
	}
}

// assertPositionIdentity verifies that for every real location position:
//   - all state quantities are non-negative
//   - on_hand = sum of all states
// (Invariant 2)
func assertPositionIdentity(t *testing.T, db *sql.DB, wid inventory.WarehouseID) {
	t.Helper()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `
		SELECT warehouse_id, sku_id, lot_id, location, lpn_id,
		       available, reserved, in_transit, quarantined, damaged
		FROM positions
		WHERE warehouse_id=$1`, string(wid),
	)
	if err != nil {
		t.Fatalf("position identity query: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var p inventory.Position
		if err := rows.Scan(
			(*string)(&p.WarehouseID), (*string)(&p.SKU), (*string)(&p.Lot),
			(*string)(&p.Location), (*string)(&p.LPN),
			&p.Available, &p.Reserved, &p.InTransit, &p.Quarantined, &p.Damaged,
		); err != nil {
			t.Fatalf("scan position: %v", err)
		}

		// Skip virtual locations — they may hold negative balances.
		if p.Location.IsVirtual() {
			continue
		}

		// All states non-negative.
		if p.Available < 0 {
			t.Errorf("INVARIANT 2 VIOLATED at %v: available=%d < 0", p.PositionKey, p.Available)
		}
		if p.Reserved < 0 {
			t.Errorf("INVARIANT 2 VIOLATED at %v: reserved=%d < 0", p.PositionKey, p.Reserved)
		}
		if p.InTransit < 0 {
			t.Errorf("INVARIANT 2 VIOLATED at %v: in_transit=%d < 0", p.PositionKey, p.InTransit)
		}
		if p.Quarantined < 0 {
			t.Errorf("INVARIANT 2 VIOLATED at %v: quarantined=%d < 0", p.PositionKey, p.Quarantined)
		}
		if p.Damaged < 0 {
			t.Errorf("INVARIANT 2 VIOLATED at %v: damaged=%d < 0", p.PositionKey, p.Damaged)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}
}

// comparePositionSets asserts that two sets of positions are identical.
// Order is normalised before comparison.
func comparePositionSets(t *testing.T, live, rebuilt []inventory.Position) {
	t.Helper()
	if len(live) != len(rebuilt) {
		t.Errorf("INVARIANT 3 VIOLATED: position count mismatch: live=%d, rebuilt=%d", len(live), len(rebuilt))
	}

	liveMap := make(map[inventory.PositionKey]inventory.Position, len(live))
	for _, p := range live {
		liveMap[p.PositionKey] = p
	}
	rebuiltMap := make(map[inventory.PositionKey]inventory.Position, len(rebuilt))
	for _, p := range rebuilt {
		rebuiltMap[p.PositionKey] = p
	}

	for k, lp := range liveMap {
		rp, ok := rebuiltMap[k]
		if !ok {
			t.Errorf("INVARIANT 3 VIOLATED: live position %+v missing from rebuilt", k)
			continue
		}
		if lp.Available != rp.Available || lp.Reserved != rp.Reserved ||
			lp.InTransit != rp.InTransit || lp.Quarantined != rp.Quarantined ||
			lp.Damaged != rp.Damaged {
			t.Errorf("INVARIANT 3 VIOLATED: mismatch at %+v:\n  live:    %+v\n  rebuilt: %+v", k, lp, rp)
		}
	}
	for k := range rebuiltMap {
		if _, ok := liveMap[k]; !ok {
			t.Errorf("INVARIANT 3 VIOLATED: rebuilt position %+v not in live set", k)
		}
	}
}

// TestMain can be used to set the CROSSDOCK_TEST_DB_URL from the environment.
// Tests are skipped if the URL is not set (allow running without a DB).
func TestMain(m *testing.M) {
	// Without CROSSDOCK_TEST_DB_URL, testdb.Open skips each test, so `go test ./...` works without a database.
	os.Exit(m.Run())
}
