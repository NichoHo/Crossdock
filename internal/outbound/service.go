// Package outbound implements orders, allocation, waves, picks, packs and
// shipments (spec 3.5, 6.2, 6.3).
package outbound

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/lib/pq"
	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbox"
	"github.com/nicholasgasior/crossdock/internal/tasks"
)

var (
	ErrNotFound       = errors.New("outbound: not found")
	ErrBadState       = errors.New("outbound: order is not in a state that allows this")
	ErrNotCancellable = errors.New("outbound: stock already picked; cancel is not possible")
	ErrWrongTask      = errors.New("outbound: wrong task kind")
	ErrNoPackStation  = errors.New("outbound: warehouse has no packing location")
)

type Service struct {
	db    *sql.DB
	store *inventory.Store
}

func NewService(db *sql.DB, store *inventory.Store) *Service {
	return &Service{db: db, store: store}
}

// retry reruns fn when Postgres aborts it for a deadlock or serialization
// failure. Every operation here is idempotent on re-run.
func retry(ctx context.Context, fn func() error) error {
	var err error
	for i := 0; i < 5; i++ {
		if err = fn(); err == nil {
			return nil
		}
		var pe *pq.Error
		if !errors.As(err, &pe) || (pe.Code != "40P01" && pe.Code != "40001") {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(i+1) * 10 * time.Millisecond):
		}
	}
	return err
}

func (s *Service) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return retry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// ---------------------------------------------------------------------------
// Orders
// ---------------------------------------------------------------------------

type OrderLine struct {
	ID           string
	SKU          inventory.SKUID
	Qty          int64
	AllocatedQty int64
}

type Order struct {
	ID          string
	WarehouseID inventory.WarehouseID
	Ref         string
	State       string
	WaveID      string
	Lines       []OrderLine
}

// CreateOrder records an order. Idempotent on (warehouse, ref): a repeat
// returns the existing order unchanged.
func (s *Service) CreateOrder(ctx context.Context, wid inventory.WarehouseID, ref string, lines []OrderLine) (Order, error) {
	if ref == "" || len(lines) == 0 {
		return Order{}, apperr.Invalid("ref and at least one line are required")
	}
	for _, l := range lines {
		if l.SKU == "" || l.Qty <= 0 {
			return Order{}, apperr.Invalid("each line needs a sku and a positive qty")
		}
	}
	var created bool
	var id string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		id = ids.New("ord")
		err := tx.QueryRowContext(ctx, `
			INSERT INTO orders (id, warehouse_id, ref) VALUES ($1,$2,$3)
			ON CONFLICT (warehouse_id, ref) DO NOTHING RETURNING id`, id, string(wid), ref).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			created = false
			return nil
		}
		if err != nil {
			return err
		}
		created = true
		for _, l := range lines {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO order_lines (id, warehouse_id, order_id, sku_id, qty) VALUES ($1,$2,$3,$4,$5)`,
				ids.New("ol"), string(wid), id, string(l.SKU), l.Qty); err != nil {
				return fmt.Errorf("outbound: insert line (sku %q): %w", l.SKU, err)
			}
		}
		return nil
	})
	if err != nil {
		return Order{}, err
	}
	if !created {
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM orders WHERE warehouse_id=$1 AND ref=$2`, string(wid), ref).Scan(&id); err != nil {
			return Order{}, err
		}
	}
	return s.GetOrder(ctx, wid, id)
}

func (s *Service) GetOrder(ctx context.Context, wid inventory.WarehouseID, id string) (Order, error) {
	o := Order{WarehouseID: wid}
	err := s.db.QueryRowContext(ctx, `SELECT id, ref, state, wave_id FROM orders WHERE warehouse_id=$1 AND id=$2`,
		string(wid), id).Scan(&o.ID, &o.Ref, &o.State, &o.WaveID)
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	if err != nil {
		return o, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, sku_id, qty, allocated_qty FROM order_lines WHERE warehouse_id=$1 AND order_id=$2 ORDER BY id`,
		string(wid), id)
	if err != nil {
		return o, err
	}
	defer rows.Close()
	for rows.Next() {
		var l OrderLine
		if err := rows.Scan(&l.ID, (*string)(&l.SKU), &l.Qty, &l.AllocatedQty); err != nil {
			return o, err
		}
		o.Lines = append(o.Lines, l)
	}
	return o, rows.Err()
}

// ---------------------------------------------------------------------------
// Allocation (the serialised decision)
// ---------------------------------------------------------------------------

// Allocate reserves stock for an order using the documented policy. Each unit
// is reserved under a position row lock, so two orders can never win the same
// unit. Partial allocation is recorded; the order reappears in Sweep. Returns
// the resulting order state.
func (s *Service) Allocate(ctx context.Context, wid inventory.WarehouseID, orderID string) (string, error) {
	var state string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var cur string
		err := tx.QueryRowContext(ctx, `SELECT state FROM orders WHERE warehouse_id=$1 AND id=$2 FOR UPDATE`,
			string(wid), orderID).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		state = cur
		if cur != "new" && cur != "partially_allocated" && cur != "unallocatable" {
			return nil // already allocated, waved, cancelled...: nothing to do
		}

		// Lines sorted by SKU so concurrent multi-line orders lock in one order.
		rows, err := tx.QueryContext(ctx, `
			SELECT id, sku_id, qty - allocated_qty FROM order_lines
			WHERE warehouse_id=$1 AND order_id=$2 ORDER BY sku_id, id FOR UPDATE`, string(wid), orderID)
		if err != nil {
			return err
		}
		type need struct {
			id  string
			sku inventory.SKUID
			n   int64
		}
		var needs []need
		for rows.Next() {
			var n need
			if err := rows.Scan(&n.id, (*string)(&n.sku), &n.n); err != nil {
				rows.Close()
				return err
			}
			needs = append(needs, n)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		var reservedAny bool
		for _, n := range needs {
			if n.n == 0 {
				continue
			}
			got, err := s.reserveLine(ctx, tx, wid, orderID, n.id, n.sku, n.n)
			if err != nil {
				return err
			}
			reservedAny = reservedAny || got > 0
		}

		var open, total int64
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(qty - allocated_qty),0), COALESCE(SUM(allocated_qty),0)
			FROM order_lines WHERE warehouse_id=$1 AND order_id=$2`, string(wid), orderID).Scan(&open, &total); err != nil {
			return err
		}
		next := "unallocatable"
		switch {
		case open == 0:
			next = "allocated"
		case total > 0:
			next = "partially_allocated"
		}
		state = next
		if next == cur && !reservedAny {
			return nil // sweep found nothing new: no state change, no event
		}
		if _, err := tx.ExecContext(ctx, `UPDATE orders SET state=$3 WHERE warehouse_id=$1 AND id=$2`,
			string(wid), orderID, next); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, string(wid), "order."+next, orderID, map[string]any{"order_id": orderID, "state": next})
	})
	return state, err
}

// reserveLine reserves up to want units of sku across positions in policy order.
func (s *Service) reserveLine(ctx context.Context, tx *sql.Tx, wid inventory.WarehouseID, orderID, lineID string, sku inventory.SKUID, want int64) (int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT p.location, p.lot_id, p.lpn_id, p.available, lt.expires_at, z.purpose = 'pick_face'
		FROM positions p
		JOIN lots lt ON lt.warehouse_id = p.warehouse_id AND lt.id = p.lot_id
		JOIN locations lo ON lo.warehouse_id = p.warehouse_id AND lo.code = p.location
		JOIN zones z ON z.id = lo.zone_id
		WHERE p.warehouse_id=$1 AND p.sku_id=$2 AND p.available > 0 AND z.purpose IN ('pick_face','bulk')`,
		string(wid), string(sku))
	if err != nil {
		return 0, err
	}
	var cands []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.Location, &c.Lot, &c.LPN, &c.Available, &c.ExpiresAt, &c.PickFace); err != nil {
			rows.Close()
			return 0, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var got int64
	for _, c := range ApplyPolicy(cands) {
		if got == want {
			break
		}
		// Re-read under lock: the candidate list is a hint, the locked row is the truth.
		var avail int64
		err := tx.QueryRowContext(ctx, `
			SELECT available FROM positions
			WHERE warehouse_id=$1 AND sku_id=$2 AND lot_id=$3 AND location=$4 AND lpn_id=$5 FOR UPDATE`,
			string(wid), string(sku), c.Lot, c.Location, c.LPN).Scan(&avail)
		if err != nil {
			return got, err
		}
		take := min(avail, want-got)
		if take <= 0 {
			continue
		}
		allocID := ids.New("alc")
		mv := inventory.Movement{
			ID: ids.New("mvt"), WarehouseID: wid, Kind: inventory.KindReserve,
			FromLocation: inventory.LocationCode(c.Location), FromState: inventory.StateAvailable,
			ToLocation: inventory.LocationCode(c.Location), ToState: inventory.StateReserved,
			SKUID: sku, LotID: inventory.LotID(c.Lot), LPNID: inventory.LPNID(c.LPN), Qty: take,
			OccurredAt: time.Now(), IdempotencyKey: "alloc-" + allocID, OrderID: orderID,
		}
		if err := s.store.AppendMovementTx(ctx, tx, mv); err != nil {
			return got, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO allocations (id, warehouse_id, order_id, order_line_id, sku_id, lot_id, location, lpn_id, qty)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			allocID, string(wid), orderID, lineID, string(sku), c.Lot, c.Location, c.LPN, take); err != nil {
			return got, err
		}
		got += take
	}
	if got > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE order_lines SET allocated_qty = allocated_qty + $3 WHERE warehouse_id=$1 AND id=$2`,
			string(wid), lineID, got); err != nil {
			return got, err
		}
	}
	return got, nil
}

// Sweep allocates every order that is new, partially allocated or unallocatable,
// oldest first. It returns how many orders changed state.
func (s *Service) Sweep(ctx context.Context, wid inventory.WarehouseID) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, state FROM orders
		WHERE warehouse_id=$1 AND state IN ('new','partially_allocated','unallocatable')
		ORDER BY created_at, id`, string(wid))
	if err != nil {
		return 0, err
	}
	type o struct{ id, state string }
	var os []o
	for rows.Next() {
		var x o
		if err := rows.Scan(&x.id, &x.state); err != nil {
			rows.Close()
			return 0, err
		}
		os = append(os, x)
	}
	rows.Close()
	changed := 0
	for _, x := range os {
		st, err := s.Allocate(ctx, wid, x.id)
		if err != nil {
			return changed, err
		}
		if st != x.state {
			changed++
		}
	}
	return changed, nil
}

// Cancel releases every reserved allocation exactly once and cancels the order
// (invariant 5). Repeating it is a no-op. It refuses once any unit is picked.
func (s *Service) Cancel(ctx context.Context, wid inventory.WarehouseID, orderID string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var state string
		err := tx.QueryRowContext(ctx, `SELECT state FROM orders WHERE warehouse_id=$1 AND id=$2 FOR UPDATE`,
			string(wid), orderID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if state == "cancelled" {
			return nil
		}
		if state == "packed" || state == "shipped" {
			return ErrNotCancellable
		}
		type alc struct {
			id, lot, loc, lpn, line string
			sku                     inventory.SKUID
			qty                     int64
			state                   string
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT id, lot_id, location, lpn_id, order_line_id, sku_id, qty, state
			FROM allocations WHERE warehouse_id=$1 AND order_id=$2 ORDER BY id FOR UPDATE`, string(wid), orderID)
		if err != nil {
			return err
		}
		var as []alc
		for rows.Next() {
			var a alc
			if err := rows.Scan(&a.id, &a.lot, &a.loc, &a.lpn, &a.line, (*string)(&a.sku), &a.qty, &a.state); err != nil {
				rows.Close()
				return err
			}
			as = append(as, a)
		}
		rows.Close()
		for _, a := range as {
			if a.state != "reserved" && a.state != "released" {
				return ErrNotCancellable
			}
		}
		for _, a := range as {
			if a.state != "reserved" {
				continue
			}
			mv := inventory.Movement{
				ID: ids.New("mvt"), WarehouseID: wid, Kind: inventory.KindRelease,
				FromLocation: inventory.LocationCode(a.loc), FromState: inventory.StateReserved,
				ToLocation: inventory.LocationCode(a.loc), ToState: inventory.StateAvailable,
				SKUID: a.sku, LotID: inventory.LotID(a.lot), LPNID: inventory.LPNID(a.lpn), Qty: a.qty,
				OccurredAt: time.Now(), IdempotencyKey: "release-" + a.id, OrderID: orderID,
			}
			if err := s.store.AppendMovementTx(ctx, tx, mv); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE allocations SET state='released' WHERE warehouse_id=$1 AND id=$2`,
				string(wid), a.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE order_lines SET allocated_qty = allocated_qty - $3 WHERE warehouse_id=$1 AND id=$2`,
				string(wid), a.line, a.qty); err != nil {
				return err
			}
			// Cancel the pick task if the wave already created one.
			var tid string
			err := tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE warehouse_id=$1 AND kind='pick' AND ref=$2`,
				string(wid), a.id).Scan(&tid)
			if err == nil {
				t, err := tasks.Lock(ctx, tx, wid, tid)
				if err != nil {
					return err
				}
				if tasks.CanTransition(t.State, inventory.TaskCancelled) {
					if err := tasks.Transition(ctx, tx, t, inventory.TaskCancelled, "", "order_cancelled"); err != nil {
						return err
					}
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE orders SET state='cancelled' WHERE warehouse_id=$1 AND id=$2`,
			string(wid), orderID); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, string(wid), "order.cancelled", orderID, map[string]any{"order_id": orderID})
	})
}

// ---------------------------------------------------------------------------
// Waves
// ---------------------------------------------------------------------------

// Wave is a released batch of orders with its pick route.
type Wave struct {
	ID            string
	RouteDistance float64
	PickTasks     int
}

// ReleaseWave groups allocated orders, computes the pick route and creates pick
// tasks in route order. Every order must be allocated or partially allocated.
func (s *Service) ReleaseWave(ctx context.Context, wid inventory.WarehouseID, orderIDs []string) (Wave, error) {
	if len(orderIDs) == 0 {
		return Wave{}, apperr.Invalid("a wave needs at least one order")
	}
	sort.Strings(orderIDs) // consistent lock order
	var w Wave
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		w = Wave{ID: ids.New("wav")}
		for _, oid := range orderIDs {
			var st string
			err := tx.QueryRowContext(ctx, `SELECT state FROM orders WHERE warehouse_id=$1 AND id=$2 FOR UPDATE`,
				string(wid), oid).Scan(&st)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if st != "allocated" && st != "partially_allocated" {
				return fmt.Errorf("%w: order %s is %s", ErrBadState, oid, st)
			}
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT id, order_id, sku_id, lot_id, location, lpn_id, qty FROM allocations
			WHERE warehouse_id=$1 AND order_id = ANY($2) AND state='reserved' ORDER BY id`,
			string(wid), pq.Array(orderIDs))
		if err != nil {
			return err
		}
		type pick struct {
			alloc, order, lot, loc, lpn string
			sku                         inventory.SKUID
			qty                         int64
		}
		var picks []pick
		for rows.Next() {
			var p pick
			if err := rows.Scan(&p.alloc, &p.order, (*string)(&p.sku), &p.lot, &p.loc, &p.lpn, &p.qty); err != nil {
				rows.Close()
				return err
			}
			picks = append(picks, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(picks) == 0 {
			return fmt.Errorf("%w: nothing reserved to pick", ErrBadState)
		}
		stops := make([]string, len(picks))
		for i, p := range picks {
			stops[i] = p.loc
		}
		order, metres, err := Route(stops)
		if err != nil {
			return err
		}
		w.RouteDistance = metres
		if _, err := tx.ExecContext(ctx, `INSERT INTO waves (id, warehouse_id, route_distance) VALUES ($1,$2,$3)`,
			w.ID, string(wid), metres); err != nil {
			return err
		}
		for seq, idx := range order {
			p := picks[idx]
			if _, _, err := tasks.Create(ctx, tx, tasks.Task{
				WarehouseID: wid, Kind: inventory.TaskPick, SKU: p.sku, Lot: inventory.LotID(p.lot),
				LPN: inventory.LPNID(p.lpn), FromLocation: inventory.LocationCode(p.loc), Qty: p.qty,
				OrderID: p.order, WaveID: w.ID, Ref: p.alloc, RouteSeq: seq + 1,
			}); err != nil {
				return err
			}
			w.PickTasks++
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE orders SET state='waved', wave_id=$3 WHERE warehouse_id=$1 AND id = ANY($2)`,
			string(wid), pq.Array(orderIDs), w.ID); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, string(wid), "wave.released", w.ID,
			map[string]any{"wave_id": w.ID, "orders": orderIDs, "route_distance_m": metres, "pick_tasks": w.PickTasks})
	})
	return w, err
}

// ---------------------------------------------------------------------------
// Pick, pack, ship
// ---------------------------------------------------------------------------

// CompletePick confirms a pick task: reserved -> in_transit, task completed.
// Repeating it is a no-op. When the last pick of an order completes, the pack
// task is created in the same transaction.
func (s *Service) CompletePick(ctx context.Context, wid inventory.WarehouseID, taskID string) error {
	return retry(ctx, func() error {
		return tasks.Do(ctx, s.db, wid, taskID, func(tx *sql.Tx, t tasks.Task) error {
			if t.Kind != inventory.TaskPick {
				return ErrWrongTask
			}
			if t.State == inventory.TaskCompleted {
				return nil
			}
			var state string
			err := tx.QueryRowContext(ctx, `SELECT state FROM allocations WHERE warehouse_id=$1 AND id=$2 FOR UPDATE`,
				string(wid), t.Ref).Scan(&state)
			if err != nil {
				return err
			}
			if state != "reserved" {
				return fmt.Errorf("%w: allocation is %s", ErrBadState, state)
			}
			mv := inventory.Movement{
				ID: ids.New("mvt"), WarehouseID: wid, Kind: inventory.KindPick,
				FromLocation: t.FromLocation, FromState: inventory.StateReserved,
				ToLocation: t.FromLocation, ToState: inventory.StateInTransit,
				SKUID: t.SKU, LotID: t.Lot, LPNID: t.LPN, Qty: t.Qty, OccurredAt: time.Now(),
				IdempotencyKey: "task-" + t.ID, TaskID: t.ID, OrderID: t.OrderID,
			}
			if err := s.store.AppendMovementTx(ctx, tx, mv); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE allocations SET state='picked' WHERE warehouse_id=$1 AND id=$2`,
				string(wid), t.Ref); err != nil {
				return err
			}
			if err := tasks.Transition(ctx, tx, t, inventory.TaskCompleted, "", ""); err != nil {
				return err
			}
			var remaining int
			if err := tx.QueryRowContext(ctx, `
				SELECT count(*) FROM allocations WHERE warehouse_id=$1 AND order_id=$2 AND state='reserved'`,
				string(wid), t.OrderID).Scan(&remaining); err != nil {
				return err
			}
			if remaining > 0 {
				return nil
			}
			var station string
			err = tx.QueryRowContext(ctx, `
				SELECT l.code FROM locations l JOIN zones z ON z.id=l.zone_id
				WHERE l.warehouse_id=$1 AND z.purpose='packing' ORDER BY l.code LIMIT 1`, string(wid)).Scan(&station)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNoPackStation
			}
			if err != nil {
				return err
			}
			var qty int64
			if err := tx.QueryRowContext(ctx, `
				SELECT COALESCE(SUM(qty),0) FROM allocations WHERE warehouse_id=$1 AND order_id=$2 AND state='picked'`,
				string(wid), t.OrderID).Scan(&qty); err != nil {
				return err
			}
			_, _, err = tasks.Create(ctx, tx, tasks.Task{
				WarehouseID: wid, Kind: inventory.TaskPack, ToLocation: inventory.LocationCode(station),
				Qty: qty, OrderID: t.OrderID, WaveID: t.WaveID, Ref: "pack-" + t.OrderID,
			})
			return err
		})
	})
}

// CompletePack confirms a pack task: every picked unit of the order moves to
// the packing station, the task completes and the order becomes packed.
func (s *Service) CompletePack(ctx context.Context, wid inventory.WarehouseID, taskID string) error {
	return retry(ctx, func() error {
		return tasks.Do(ctx, s.db, wid, taskID, func(tx *sql.Tx, t tasks.Task) error {
			if t.Kind != inventory.TaskPack {
				return ErrWrongTask
			}
			if t.State == inventory.TaskCompleted {
				return nil
			}
			rows, err := tx.QueryContext(ctx, `
				SELECT id, sku_id, lot_id, location, lpn_id, qty FROM allocations
				WHERE warehouse_id=$1 AND order_id=$2 AND state='picked' ORDER BY id FOR UPDATE`,
				string(wid), t.OrderID)
			if err != nil {
				return err
			}
			type a struct {
				id, lot, loc, lpn string
				sku               inventory.SKUID
				qty               int64
			}
			var as []a
			for rows.Next() {
				var x a
				if err := rows.Scan(&x.id, (*string)(&x.sku), &x.lot, &x.loc, &x.lpn, &x.qty); err != nil {
					rows.Close()
					return err
				}
				as = append(as, x)
			}
			rows.Close()
			if len(as) == 0 {
				return fmt.Errorf("%w: nothing picked to pack", ErrBadState)
			}
			for _, x := range as {
				mv := inventory.Movement{
					ID: ids.New("mvt"), WarehouseID: wid, Kind: inventory.KindPack,
					FromLocation: inventory.LocationCode(x.loc), FromState: inventory.StateInTransit,
					ToLocation: t.ToLocation, ToState: inventory.StateInTransit,
					SKUID: x.sku, LotID: inventory.LotID(x.lot), LPNID: inventory.LPNID(x.lpn), Qty: x.qty,
					OccurredAt: time.Now(), IdempotencyKey: "task-" + t.ID + "-" + x.id, TaskID: t.ID, OrderID: t.OrderID,
				}
				if err := s.store.AppendMovementTx(ctx, tx, mv); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE allocations SET state='packed' WHERE warehouse_id=$1 AND id=$2`,
					string(wid), x.id); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE orders SET state='packed' WHERE warehouse_id=$1 AND id=$2`,
				string(wid), t.OrderID); err != nil {
				return err
			}
			return tasks.Transition(ctx, tx, t, inventory.TaskCompleted, "", "")
		})
	})
}

// Shipment is a carrier handoff.
type Shipment struct {
	ID             string
	OrderID        string
	Carrier        string
	Tracking       string
	CODAmountMinor *int64
}

// Ship hands a packed order to the carrier: packing station -> CARRIER, a
// shipment row, and shipment.dispatched. Repeating it returns the existing
// shipment.
func (s *Service) Ship(ctx context.Context, wid inventory.WarehouseID, orderID, carrier, tracking string, codMinor *int64) (Shipment, error) {
	if carrier == "" || tracking == "" {
		return Shipment{}, apperr.Invalid("carrier and tracking are required")
	}
	var sh Shipment
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var state string
		err := tx.QueryRowContext(ctx, `SELECT state FROM orders WHERE warehouse_id=$1 AND id=$2 FOR UPDATE`,
			string(wid), orderID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if state == "shipped" {
			return tx.QueryRowContext(ctx, `
				SELECT id, order_id, carrier, tracking, cod_amount_minor FROM shipments WHERE warehouse_id=$1 AND order_id=$2`,
				string(wid), orderID).Scan(&sh.ID, &sh.OrderID, &sh.Carrier, &sh.Tracking, &sh.CODAmountMinor)
		}
		if state != "packed" {
			return fmt.Errorf("%w: order is %s, want packed", ErrBadState, state)
		}
		var station string
		if err := tx.QueryRowContext(ctx, `
			SELECT to_location FROM tasks WHERE warehouse_id=$1 AND kind='pack' AND ref=$2`,
			string(wid), "pack-"+orderID).Scan(&station); err != nil {
			return err
		}
		sh = Shipment{ID: ids.New("shp"), OrderID: orderID, Carrier: carrier, Tracking: tracking, CODAmountMinor: codMinor}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO shipments (id, warehouse_id, order_id, carrier, tracking, cod_amount_minor)
			VALUES ($1,$2,$3,$4,$5,$6)`, sh.ID, string(wid), orderID, carrier, tracking, codMinor); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT id, sku_id, lot_id, lpn_id, qty FROM allocations
			WHERE warehouse_id=$1 AND order_id=$2 AND state='packed' ORDER BY id FOR UPDATE`, string(wid), orderID)
		if err != nil {
			return err
		}
		type a struct {
			id, lot, lpn string
			sku          inventory.SKUID
			qty          int64
		}
		var as []a
		for rows.Next() {
			var x a
			if err := rows.Scan(&x.id, (*string)(&x.sku), &x.lot, &x.lpn, &x.qty); err != nil {
				rows.Close()
				return err
			}
			as = append(as, x)
		}
		rows.Close()
		for _, x := range as {
			mv := inventory.Movement{
				ID: ids.New("mvt"), WarehouseID: wid, Kind: inventory.KindShip,
				FromLocation: inventory.LocationCode(station), FromState: inventory.StateInTransit,
				ToLocation: inventory.VirtualCarrier, ToState: inventory.StateAvailable,
				SKUID: x.sku, LotID: inventory.LotID(x.lot), LPNID: inventory.LPNID(x.lpn), Qty: x.qty,
				OccurredAt: time.Now(), IdempotencyKey: "ship-" + x.id, OrderID: orderID, ShipmentID: sh.ID,
			}
			if err := s.store.AppendMovementTx(ctx, tx, mv); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE allocations SET state='shipped' WHERE warehouse_id=$1 AND id=$2`,
				string(wid), x.id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE orders SET state='shipped' WHERE warehouse_id=$1 AND id=$2`,
			string(wid), orderID); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, string(wid), "shipment.dispatched", sh.ID, map[string]any{
			"shipment_id": sh.ID, "order_id": orderID, "carrier": carrier, "tracking": tracking, "cod_amount_minor": codMinor,
		})
	})
	return sh, err
}
