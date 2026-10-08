package generate

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"time"

	"github.com/nicholasgasior/crossdock/internal/inventory"
)

// Config controls the synthetic data generation.
type Config struct {
	// Seed is the random number generator seed. The same seed produces the
	// same data. Use 0 to use a time-based seed.
	Seed int64

	// WarehouseID is the ID to assign to the generated warehouse.
	// If empty, a random ID is generated.
	WarehouseID string

	// WarehouseName is the display name for the warehouse.
	WarehouseName string

	// Aisles is the number of storage aisles to generate.
	// Each aisle has BaysPerAisle × LevelsPerBay × SlotsPerLevel locations.
	Aisles int

	// BaysPerAisle is the number of bays (columns) per aisle.
	BaysPerAisle int

	// LevelsPerBay is the number of levels (rows) per bay column.
	LevelsPerBay int

	// SlotsPerLevel is the number of individual slots per level.
	SlotsPerLevel int

	// SKUCount is the number of SKUs to generate.
	SKUCount int

	// OrderCount is the number of order cycles (receive → ship) to simulate.
	OrderCount int

	// LotTrackedFraction is the fraction of SKUs that are lot-tracked (0–1).
	LotTrackedFraction float64

	// VelocityDistribution controls what fraction of SKUs get each class.
	// Must sum to 1.0. Defaults to {A:0.20, B:0.30, C:0.50}.
	VelocityDistribution struct {
		A, B, C float64
	}
}

// DefaultConfig returns a Config with sensible defaults for a medium-sized demo.
func DefaultConfig() Config {
	c := Config{
		Seed:               42,
		WarehouseName:      "Demo Warehouse",
		Aisles:             5,
		BaysPerAisle:       10,
		LevelsPerBay:       4,
		SlotsPerLevel:      2,
		SKUCount:           50,
		OrderCount:         20,
		LotTrackedFraction: 0.4,
	}
	c.VelocityDistribution.A = 0.20
	c.VelocityDistribution.B = 0.30
	c.VelocityDistribution.C = 0.50
	return c
}

// Result holds all generated entities so callers can inspect or verify the
// generated data without querying the database.
type Result struct {
	Warehouse inventory.Warehouse
	Zones     []inventory.Zone
	Locations []inventory.Location
	SKUs      []inventory.SKU
	Lots      []inventory.Lot
	// MovementCount is the total number of movements generated.
	MovementCount int
}

// Run generates synthetic warehouse data and writes it to the database.
// All writes happen inside a single transaction; if any write fails the
// transaction is rolled back.
//
// The store is used for appending movements (which also update positions).
// Direct INSERTs for structural data (warehouses, zones, etc.) go through db.
func Run(ctx context.Context, db *sql.DB, store *inventory.Store, cfg Config) (*Result, error) {
	if cfg.Seed == 0 {
		cfg.Seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(cfg.Seed))

	// Generate the warehouse ID if not provided.
	if cfg.WarehouseID == "" {
		cfg.WarehouseID = fmt.Sprintf("wh-%08x", rng.Uint32())
	}
	wid := inventory.WarehouseID(cfg.WarehouseID)

	// -----------------------------------------------------------------------
	// Step 1: Write structural data (warehouse, zones, locations, SKUs, lots)
	// in a single transaction.
	// -----------------------------------------------------------------------
	var result Result

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("generate: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Warehouse.
	wh := inventory.Warehouse{ID: wid, Name: cfg.WarehouseName, TimeZone: "UTC"}
	if err := insertWarehouse(ctx, tx, wh); err != nil {
		return nil, fmt.Errorf("generate: insert warehouse: %w", err)
	}
	result.Warehouse = wh

	// Zones.
	zones := generateZones(wid, rng)
	for _, z := range zones {
		if err := insertZone(ctx, tx, z); err != nil {
			return nil, fmt.Errorf("generate: insert zone %s: %w", z.ID, err)
		}
	}
	result.Zones = zones

	// Physical locations.
	locations := generateLocations(wid, zones, cfg, rng)
	for _, l := range locations {
		if err := insertLocation(ctx, tx, l); err != nil {
			return nil, fmt.Errorf("generate: insert location %s: %w", l.Code, err)
		}
	}
	result.Locations = locations

	// SKUs.
	skus := generateSKUs(wid, cfg, rng)
	for _, s := range skus {
		if err := insertSKU(ctx, tx, s); err != nil {
			return nil, fmt.Errorf("generate: insert SKU %s: %w", s.ID, err)
		}
	}
	result.SKUs = skus

	// Lots (one lot per SKU for simplicity in Phase 0; Phase 1 can add multi-lot).
	lots := generateLots(wid, skus, rng)
	for _, l := range lots {
		if err := insertLot(ctx, tx, l); err != nil {
			return nil, fmt.Errorf("generate: insert lot %s: %w", l.ID, err)
		}
	}
	result.Lots = lots

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("generate: commit structural data: %w", err)
	}

	// -----------------------------------------------------------------------
	// Step 2: Generate movements — receive → putaway → reserve → pick → ship.
	// Each order cycle involves a random SKU and quantity.
	// -----------------------------------------------------------------------
	pickFaceLocations := filterByZonePurpose(locations, zones, inventory.ZonePickFace)
	if len(pickFaceLocations) == 0 {
		// Fall back to all locations if no pick face defined.
		pickFaceLocations = locations
	}

	movementCount := 0
	for i := 0; i < cfg.OrderCount; i++ {
		sku := skus[rng.Intn(len(skus))]
		lot := findLot(lots, sku.ID)
		if lot == nil {
			continue // shouldn't happen
		}
		loc := pickFaceLocations[rng.Intn(len(pickFaceLocations))]
		qty := int64(rng.Intn(20) + 1)

		// Build a receive → ship sequence for this order cycle.
		mvts := buildOrderCycle(wid, sku.ID, lot.ID, loc.Code, qty, rng, i)
		for _, m := range mvts {
			if err := store.AppendMovement(ctx, m); err != nil {
				return nil, fmt.Errorf("generate: order cycle %d movement %s: %w", i, m.Kind, err)
			}
			movementCount++
		}
	}
	result.MovementCount = movementCount

	return &result, nil
}

// ---------------------------------------------------------------------------
// Generation helpers
// ---------------------------------------------------------------------------

// generateZones creates zones for a warehouse: receiving, bulk, pick_face,
// packing, returns, quarantine.
func generateZones(wid inventory.WarehouseID, _ *rand.Rand) []inventory.Zone {
	purposes := []inventory.ZonePurpose{
		inventory.ZoneReceiving,
		inventory.ZoneBulk,
		inventory.ZonePickFace,
		inventory.ZonePacking,
		inventory.ZoneReturns,
		inventory.ZoneQuarantine,
	}
	names := map[inventory.ZonePurpose]string{
		inventory.ZoneReceiving:  "Receiving Dock",
		inventory.ZoneBulk:       "Bulk Storage",
		inventory.ZonePickFace:   "Pick Face",
		inventory.ZonePacking:    "Packing Station",
		inventory.ZoneReturns:    "Returns",
		inventory.ZoneQuarantine: "Quarantine",
	}
	zones := make([]inventory.Zone, len(purposes))
	for i, p := range purposes {
		zones[i] = inventory.Zone{
			ID:          fmt.Sprintf("zone-%s-%s", wid, p),
			WarehouseID: wid,
			Name:        names[p],
			Purpose:     p,
		}
	}
	return zones
}

// generateLocations creates physical locations for the storage aisles.
// Aisles are labelled A–Z, bays 01–99, levels A–Z, slots 1–9.
func generateLocations(wid inventory.WarehouseID, zones []inventory.Zone, cfg Config, rng *rand.Rand) []inventory.Location {
	// Find the pick-face and bulk zone IDs.
	var pickFaceZoneID, bulkZoneID, receivingZoneID, packingZoneID, returnsZoneID, quarantineZoneID string
	for _, z := range zones {
		switch z.Purpose {
		case inventory.ZonePickFace:
			pickFaceZoneID = z.ID
		case inventory.ZoneBulk:
			bulkZoneID = z.ID
		case inventory.ZoneReceiving:
			receivingZoneID = z.ID
		case inventory.ZonePacking:
			packingZoneID = z.ID
		case inventory.ZoneReturns:
			returnsZoneID = z.ID
		case inventory.ZoneQuarantine:
			quarantineZoneID = z.ID
		}
	}
	_ = rng // available for future use

	var locs []inventory.Location
	aisleLetters := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

	for a := 0; a < cfg.Aisles && a < len(aisleLetters); a++ {
		aisleLetter := string(aisleLetters[a])
		for bay := 1; bay <= cfg.BaysPerAisle; bay++ {
			for level := 0; level < cfg.LevelsPerBay && level < len(aisleLetters); level++ {
				levelLetter := string(aisleLetters[level])
				for slot := 1; slot <= cfg.SlotsPerLevel; slot++ {
					code := inventory.LocationCode(
						fmt.Sprintf("%s%02d-%02d-%s-%d", aisleLetter, a+1, bay, levelLetter, slot),
					)
					// Assign first two aisles as pick face, rest as bulk.
					zoneID := bulkZoneID
					if a < 2 {
						zoneID = pickFaceZoneID
					}
					locs = append(locs, inventory.Location{
						Code:        code,
						WarehouseID: wid,
						ZoneID:      zoneID,
						MaxUnits:    50,
					})
				}
			}
		}
	}

	// Add one receiving location and one packing location.
	locs = append(locs,
		inventory.Location{Code: "A00-00-A-1", WarehouseID: wid, ZoneID: receivingZoneID, MaxUnits: 200},
		inventory.Location{Code: "P00-00-A-1", WarehouseID: wid, ZoneID: packingZoneID, MaxUnits: 500},
		inventory.Location{Code: "R00-00-A-1", WarehouseID: wid, ZoneID: returnsZoneID, MaxUnits: 500},
		inventory.Location{Code: "Q00-00-A-1", WarehouseID: wid, ZoneID: quarantineZoneID, MaxUnits: 500},
	)

	return locs
}

// generateSKUs creates a catalogue of SKUs with a velocity distribution.
func generateSKUs(wid inventory.WarehouseID, cfg Config, rng *rand.Rand) []inventory.SKU {
	skus := make([]inventory.SKU, cfg.SKUCount)
	aCount := int(float64(cfg.SKUCount) * cfg.VelocityDistribution.A)
	bCount := int(float64(cfg.SKUCount) * cfg.VelocityDistribution.B)

	for i := 0; i < cfg.SKUCount; i++ {
		var vc string
		switch {
		case i < aCount:
			vc = "A"
		case i < aCount+bCount:
			vc = "B"
		default:
			vc = "C"
		}
		isLotTracked := rng.Float64() < cfg.LotTrackedFraction
		skus[i] = inventory.SKU{
			ID:            inventory.SKUID(fmt.Sprintf("SKU-%04d", i+1)),
			WarehouseID:   wid,
			Barcode:       fmt.Sprintf("%013d", 4900000000000+int64(i)),
			Name:          fmt.Sprintf("Product %04d", i+1),
			WeightG:       rng.Intn(5000) + 100,
			VolumeCM3:     int64(rng.Intn(10000) + 100),
			IsLotTracked:  isLotTracked,
			VelocityClass: vc,
		}
	}
	return skus
}

// generateLots creates one lot per SKU (one receipt for Phase 0).
func generateLots(wid inventory.WarehouseID, skus []inventory.SKU, rng *rand.Rand) []inventory.Lot {
	lots := make([]inventory.Lot, len(skus))
	base := time.Now().AddDate(0, -3, 0) // 3 months ago
	for i, s := range skus {
		receivedAt := base.Add(time.Duration(rng.Intn(90)) * 24 * time.Hour)
		var expiresAt time.Time
		if s.IsLotTracked && rng.Float64() < 0.5 {
			expiresAt = receivedAt.AddDate(1, 0, 0)
		}
		lots[i] = inventory.Lot{
			ID:          inventory.LotID(fmt.Sprintf("LOT-%04d", i+1)),
			SKUID:       s.ID,
			WarehouseID: wid,
			ReceivedAt:  receivedAt,
			ExpiresAt:   expiresAt,
		}
	}
	return lots
}

// buildOrderCycle generates movements for one receive → ship cycle.
func buildOrderCycle(
	wid inventory.WarehouseID,
	skuID inventory.SKUID,
	lotID inventory.LotID,
	loc inventory.LocationCode,
	qty int64,
	rng *rand.Rand,
	cycleIdx int,
) []inventory.Movement {
	now := time.Now().Add(-time.Duration(rng.Intn(7200)) * time.Second)
	pfx := fmt.Sprintf("gen-%d-%08x", cycleIdx, rng.Uint32())

	// receive → available at pick face
	receive := inventory.Movement{
		ID:           pfx + "-recv",
		WarehouseID:  wid,
		Kind:         inventory.KindReceive,
		FromLocation: inventory.VirtualSupplier,
		FromState:    inventory.StateAvailable,
		ToLocation:   loc,
		ToState:      inventory.StateAvailable,
		SKUID:        skuID,
		LotID:        lotID,
		Qty:          qty,
		OccurredAt:   now,
	}

	// reserve → available→reserved at same location
	reserve := inventory.Movement{
		ID:             pfx + "-rsrv",
		WarehouseID:    wid,
		Kind:           inventory.KindReserve,
		FromLocation:   loc,
		FromState:      inventory.StateAvailable,
		ToLocation:     loc,
		ToState:        inventory.StateReserved,
		SKUID:          skuID,
		LotID:          lotID,
		Qty:            qty,
		OccurredAt:     now.Add(1 * time.Minute),
		IdempotencyKey: pfx + "-rsrv",
	}

	// pick → reserved→in_transit at same location
	pick := inventory.Movement{
		ID:           pfx + "-pick",
		WarehouseID:  wid,
		Kind:         inventory.KindPick,
		FromLocation: loc,
		FromState:    inventory.StateReserved,
		ToLocation:   loc,
		ToState:      inventory.StateInTransit,
		SKUID:        skuID,
		LotID:        lotID,
		Qty:          qty,
		OccurredAt:   now.Add(2 * time.Minute),
	}

	// ship → in_transit at location → carrier virtual location
	ship := inventory.Movement{
		ID:           pfx + "-ship",
		WarehouseID:  wid,
		Kind:         inventory.KindShip,
		FromLocation: loc,
		FromState:    inventory.StateInTransit,
		ToLocation:   inventory.VirtualCarrier,
		ToState:      inventory.StateAvailable,
		SKUID:        skuID,
		LotID:        lotID,
		Qty:          qty,
		OccurredAt:   now.Add(3 * time.Minute),
	}

	return []inventory.Movement{receive, reserve, pick, ship}
}

// filterByZonePurpose returns locations whose zone has the given purpose.
func filterByZonePurpose(locs []inventory.Location, zones []inventory.Zone, purpose inventory.ZonePurpose) []inventory.Location {
	zoneIDs := make(map[string]struct{})
	for _, z := range zones {
		if z.Purpose == purpose {
			zoneIDs[z.ID] = struct{}{}
		}
	}
	var out []inventory.Location
	for _, l := range locs {
		if _, ok := zoneIDs[l.ZoneID]; ok {
			out = append(out, l)
		}
	}
	return out
}

// findLot finds the first lot for a given SKU.
func findLot(lots []inventory.Lot, skuID inventory.SKUID) *inventory.Lot {
	for i := range lots {
		if lots[i].SKUID == skuID {
			return &lots[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Database insert helpers (using direct SQL to keep the generate package
// independent of higher-level domain services).
// ---------------------------------------------------------------------------

func insertWarehouse(ctx context.Context, tx *sql.Tx, w inventory.Warehouse) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO warehouses (id, name, time_zone) VALUES ($1, $2, $3)`,
		string(w.ID), w.Name, w.TimeZone,
	)
	return err
}

func insertZone(ctx context.Context, tx *sql.Tx, z inventory.Zone) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO zones (id, warehouse_id, name, purpose) VALUES ($1, $2, $3, $4)`,
		z.ID, string(z.WarehouseID), z.Name, string(z.Purpose),
	)
	return err
}

func insertLocation(ctx context.Context, tx *sql.Tx, l inventory.Location) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO locations (code, warehouse_id, zone_id, max_units, max_volume_cm3) VALUES ($1,$2,$3,$4,$5)`,
		string(l.Code), string(l.WarehouseID), l.ZoneID, l.MaxUnits, l.MaxVolumeCM3,
	)
	return err
}

func insertSKU(ctx context.Context, tx *sql.Tx, s inventory.SKU) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO skus (id, warehouse_id, barcode, name, weight_g, volume_cm3, is_lot_tracked, velocity_class)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		string(s.ID), string(s.WarehouseID), s.Barcode, s.Name, s.WeightG, s.VolumeCM3, s.IsLotTracked, s.VelocityClass,
	)
	return err
}

func insertLot(ctx context.Context, tx *sql.Tx, l inventory.Lot) error {
	var expiresAt *time.Time
	if !l.ExpiresAt.IsZero() {
		t := l.ExpiresAt
		expiresAt = &t
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO lots (id, sku_id, warehouse_id, received_at, expires_at) VALUES ($1,$2,$3,$4,$5)`,
		string(l.ID), string(l.SKUID), string(l.WarehouseID), l.ReceivedAt, expiresAt,
	)
	return err
}
