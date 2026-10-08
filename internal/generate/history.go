package generate

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbound"
)

// HistoryConfig shapes the pick history the slotting job learns from.
type HistoryConfig struct {
	Seed          uint64
	SKUs          int   // SKUs given a pick-face location and picked
	Waves         int   // waves of picking spread over Days
	OrdersPerWave int
	Days          int   // how far back the history reaches
	UnitsPerSKU   int64 // starting stock at each pick face
}

func DefaultHistoryConfig() HistoryConfig {
	return HistoryConfig{Seed: 1, SKUs: 40, Waves: 120, OrdersPerWave: 8, Days: 60, UnitsPerSKU: 1500}
}

// HistoryResult reports what was written.
type HistoryResult struct {
	SKUs, Waves, Picks int
	// MeanRouteM is the average real route length of the generated waves.
	MeanRouteM float64
}

// GenerateHistory writes a believable pick history into an existing warehouse:
//   - demand is skewed (a few SKUs account for most picks, a Zipf curve),
//   - some SKUs are ordered together (bundles), and
//   - the pick faces were filled in a random order, so the fast movers are
//     scattered across the floor. That is what slotting exists to fix.
//
// Every pick is a real task and real reserve, pick and ship movements, so the
// stock invariants hold afterwards. Pick faces used are enlarged to hold
// UnitsPerSKU, and stock is received into them first.
func GenerateHistory(ctx context.Context, db *sql.DB, store *inventory.Store, wid inventory.WarehouseID, cfg HistoryConfig) (*HistoryResult, error) {
	rng := rand.New(rand.NewPCG(cfg.Seed, 0x5107))

	var faces []string
	rows, err := db.QueryContext(ctx, `
		SELECT l.code FROM locations l JOIN zones z ON z.id=l.zone_id
		WHERE l.warehouse_id=$1 AND z.purpose='pick_face' ORDER BY l.code`, string(wid))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return nil, err
		}
		faces = append(faces, c)
	}
	rows.Close()
	if len(faces) < cfg.SKUs {
		return nil, fmt.Errorf("generate: %d pick-face locations cannot hold %d SKUs", len(faces), cfg.SKUs)
	}

	type skuInfo struct{ id, lot, loc string }
	srows, err := db.QueryContext(ctx, `
		SELECT s.id, (SELECT l.id FROM lots l WHERE l.warehouse_id=s.warehouse_id AND l.sku_id=s.id ORDER BY l.id LIMIT 1)
		FROM skus s WHERE s.warehouse_id=$1 ORDER BY s.id LIMIT $2`, string(wid), cfg.SKUs)
	if err != nil {
		return nil, err
	}
	var skus []skuInfo
	for srows.Next() {
		var s skuInfo
		var lot sql.NullString
		if err := srows.Scan(&s.id, &lot); err != nil {
			srows.Close()
			return nil, err
		}
		if !lot.Valid {
			srows.Close()
			return nil, fmt.Errorf("generate: SKU %s has no lot", s.id)
		}
		s.lot = lot.String
		skus = append(skus, s)
	}
	srows.Close()
	if len(skus) < cfg.SKUs {
		return nil, fmt.Errorf("generate: warehouse has %d SKUs, need %d", len(skus), cfg.SKUs)
	}

	// Fill pick faces in a random order, one SKU per face.
	perm := rng.Perm(len(faces))
	for i := range skus {
		skus[i].loc = faces[perm[i]]
		if _, err := db.ExecContext(ctx, `UPDATE locations SET max_units=$3 WHERE warehouse_id=$1 AND code=$2 AND max_units < $3`,
			string(wid), skus[i].loc, cfg.UnitsPerSKU*2); err != nil {
			return nil, err
		}
		err := store.AppendMovement(ctx, inventory.Movement{
			ID: ids.New("mvt"), WarehouseID: wid, Kind: inventory.KindReceive,
			FromLocation: inventory.VirtualSupplier, FromState: inventory.StateAvailable,
			ToLocation: inventory.LocationCode(skus[i].loc), ToState: inventory.StateAvailable,
			SKUID: inventory.SKUID(skus[i].id), LotID: inventory.LotID(skus[i].lot), Qty: cfg.UnitsPerSKU,
			OccurredAt: time.Now().Add(-time.Duration(cfg.Days+1) * 24 * time.Hour), IdempotencyKey: "hist-stock-" + string(wid) + "-" + skus[i].id,
		})
		if err != nil && err != inventory.ErrDuplicateMovement {
			return nil, err
		}
	}

	// Demand: a Zipf curve over a random ranking, so popularity is unrelated to position.
	rank := rng.Perm(len(skus))
	weight := make([]float64, len(skus))
	total := 0.0
	for i := range skus {
		weight[i] = 1 / float64(rank[i]+1)
		total += weight[i]
	}
	draw := func(exclude map[int]bool) int {
		for {
			x := rng.Float64() * total
			for i, w := range weight {
				if x -= w; x <= 0 {
					if !exclude[i] {
						return i
					}
					break
				}
			}
		}
	}
	// A handful of bundles that are usually ordered together.
	var bundles [][]int
	for b := 0; b < 8; b++ {
		used := map[int]bool{}
		size := 3 + rng.IntN(2)
		var bundle []int
		for len(bundle) < size {
			i := draw(used)
			used[i] = true
			bundle = append(bundle, i)
		}
		bundles = append(bundles, bundle)
	}

	res := &HistoryResult{SKUs: len(skus)}
	start := time.Now().Add(-time.Duration(cfg.Days) * 24 * time.Hour)
	span := time.Duration(cfg.Days) * 24 * time.Hour
	for w := 0; w < cfg.Waves; w++ {
		at := start.Add(span * time.Duration(w) / time.Duration(cfg.Waves))
		waveID := fmt.Sprintf("wav-hist-%s-%04d", string(wid), w)
		type line struct {
			sku   int
			order string
		}
		var lines []line
		for o := 0; o < cfg.OrdersPerWave; o++ {
			orderID := fmt.Sprintf("ord-hist-%s-%04d-%02d", string(wid), w, o)
			chosen := map[int]bool{}
			if rng.Float64() < 0.5 {
				b := bundles[rng.IntN(len(bundles))]
				for _, i := range rng.Perm(len(b))[:2+rng.IntN(len(b)-1)] {
					chosen[b[i]] = true
				}
			} else {
				for n := 1 + rng.IntN(3); len(chosen) < n; {
					chosen[draw(chosen)] = true
				}
			}
			var idx []int
			for i := range chosen {
				idx = append(idx, i)
			}
			sort.Ints(idx)
			for _, i := range idx {
				lines = append(lines, line{i, orderID})
			}
		}
		stops := make([]string, len(lines))
		for i, l := range lines {
			stops[i] = skus[l.sku].loc
		}
		order, metres, err := outbound.Route(stops)
		if err != nil {
			return nil, err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO waves (id, warehouse_id, route_distance, created_at) VALUES ($1,$2,$3,$4)`, waveID, string(wid), metres, at); err != nil {
			tx.Rollback() //nolint:errcheck
			return nil, err
		}
		for seq, li := range order {
			l, s := lines[li], skus[lines[li].sku]
			taskID := ids.New("tsk")
			done := at.Add(time.Duration(seq+1) * 20 * time.Second)
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO tasks (id, warehouse_id, kind, state, assigned_to, sku_id, lot_id, from_location, qty, order_id, wave_id, ref, route_seq, created_at, updated_at, completed_at)
				VALUES ($1,$2,'pick','completed','history',$3,$4,$5,1,$6,$7,$8,$9,$10,$11,$11)`,
				taskID, string(wid), s.id, s.lot, s.loc, l.order, waveID, "hist-"+taskID, seq+1, at, done); err != nil {
				tx.Rollback() //nolint:errcheck
				return nil, err
			}
			loc := inventory.LocationCode(s.loc)
			steps := []inventory.Movement{
				{Kind: inventory.KindReserve, FromLocation: loc, FromState: inventory.StateAvailable, ToLocation: loc, ToState: inventory.StateReserved},
				{Kind: inventory.KindPick, FromLocation: loc, FromState: inventory.StateReserved, ToLocation: loc, ToState: inventory.StateInTransit, TaskID: taskID},
				{Kind: inventory.KindShip, FromLocation: loc, FromState: inventory.StateInTransit, ToLocation: inventory.VirtualCarrier, ToState: inventory.StateAvailable},
			}
			for n, m := range steps {
				m.ID, m.WarehouseID, m.SKUID, m.LotID, m.Qty = ids.New("mvt"), wid, inventory.SKUID(s.id), inventory.LotID(s.lot), 1
				m.OccurredAt, m.OrderID, m.IdempotencyKey = done, l.order, fmt.Sprintf("hist-%s-%d", taskID, n)
				if err := store.AppendMovementTx(ctx, tx, m); err != nil {
					tx.Rollback() //nolint:errcheck
					return nil, err
				}
			}
			res.Picks++
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		res.Waves++
		res.MeanRouteM += metres
	}
	if res.Waves > 0 {
		res.MeanRouteM /= float64(res.Waves)
	}
	return res, nil
}
