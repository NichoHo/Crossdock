// Package returns is reverse logistics (spec 9.3): a return authorisation, the
// parcel scanned in at intake, and a disposition task per returned line.
package returns

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inbound"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbox"
	"github.com/nicholasgasior/crossdock/internal/tasks"
)

var (
	ErrNotFound         = errors.New("returns: not found")
	ErrNoLocation       = errors.New("returns: the warehouse has no location in that zone")
	ErrWrongTask        = errors.New("returns: not a disposition task")
	ErrNothingAuthorised = errors.New("returns: no return is expected for that tracking number")
)

// Conditions an operator can mark a returned unit with.
const (
	Good    = "good"
	Damaged = "damaged"
	Unknown = "unknown"
)

// Decisions for a returned line.
const (
	Restock    = "restock"
	Quarantine = "quarantine"
	Scrap      = "scrap"
)

type Service struct {
	db    *sql.DB
	store *inventory.Store
	now   func() time.Time
}

func NewService(db *sql.DB, store *inventory.Store) *Service {
	return &Service{db: db, store: store, now: time.Now}
}

func (s *Service) SetClock(now func() time.Time) { s.now = now }

// CreateRA opens a return authorisation for a shipment inside the caller's
// transaction (a rejected COD outcome creates one automatically). It is
// idempotent per shipment, and creates the intake task that puts the expected
// parcel on the task board.
func CreateRA(ctx context.Context, tx *sql.Tx, wid inventory.WarehouseID, shipmentID, orderID, reason string, at time.Time) (id string, created bool, err error) {
	id = ids.New("ra")
	err = tx.QueryRowContext(ctx, `
		INSERT INTO return_authorisations (id, warehouse_id, shipment_id, order_id, reason, created_at) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (warehouse_id, shipment_id) DO NOTHING RETURNING id`,
		id, string(wid), shipmentID, orderID, reason, at).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT id FROM return_authorisations WHERE warehouse_id=$1 AND shipment_id=$2`, string(wid), shipmentID).Scan(&id)
		return id, false, err
	}
	if err != nil {
		return "", false, err
	}
	if _, _, err := tasks.Create(ctx, tx, tasks.Task{WarehouseID: wid, Kind: inventory.TaskReturnIntake, OrderID: orderID,
		Ref: id, Reason: reason}); err != nil {
		return "", false, err
	}
	return id, true, outbox.Emit(ctx, tx, string(wid), "return.authorised", shipmentID,
		map[string]any{"return_authorisation_id": id, "shipment_id": shipmentID, "order_id": orderID, "reason": reason})
}

// IntakeLine is one kind of item in a returned parcel.
type IntakeLine struct {
	SKU       string
	Lot       string // optional: defaults to the lot that was shipped
	Qty       int64
	Condition string
}

// Receipt is what intake produced.
type Receipt struct {
	RAID      string
	Lines     []string // return line ids
	Tasks     []string // disposition task ids
	Duplicate bool
}

// Receive scans a returned parcel in: a return_receive movement from
// RETURN_INTAKE to the returns zone for each line, and a disposition task for
// each. Repeating the same key is a no-op. A parcel can only bring back units
// that were shipped.
func (s *Service) Receive(ctx context.Context, wid inventory.WarehouseID, operator, tracking, key string, lines []IntakeLine) (Receipt, error) {
	if key == "" || tracking == "" || len(lines) == 0 {
		return Receipt{}, apperr.Invalid("a tracking number, an idempotency key and at least one line are required")
	}
	for _, l := range lines {
		if l.SKU == "" || l.Qty <= 0 || (l.Condition != Good && l.Condition != Damaged && l.Condition != Unknown) {
			return Receipt{}, apperr.Invalid("each line needs a sku, a positive qty and a condition of good, damaged or unknown")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	var rc Receipt
	// Repeat of an intake we already did?
	prev, err := tx.QueryContext(ctx, `SELECT ra_id, id FROM return_lines WHERE warehouse_id=$1 AND intake_key=$2 ORDER BY id`, string(wid), key)
	if err != nil {
		return rc, err
	}
	for prev.Next() {
		var ra, id string
		if err := prev.Scan(&ra, &id); err != nil {
			prev.Close()
			return rc, err
		}
		rc.RAID, rc.Lines, rc.Duplicate = ra, append(rc.Lines, id), true
	}
	prev.Close()
	if rc.Duplicate {
		return rc, nil
	}

	var raID, orderID, shipmentID, state string
	err = tx.QueryRowContext(ctx, `
		SELECT r.id, r.order_id, r.shipment_id, r.state FROM return_authorisations r
		JOIN shipments sh ON sh.warehouse_id=r.warehouse_id AND sh.id=r.shipment_id
		WHERE r.warehouse_id=$1 AND sh.tracking=$2 FOR UPDATE OF r`, string(wid), tracking).Scan(&raID, &orderID, &shipmentID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return rc, ErrNothingAuthorised
	}
	if err != nil {
		return rc, err
	}
	if state == "dispositioned" || state == "cancelled" {
		return rc, apperr.Invalid("this return is already %s", state)
	}
	loc, err := zoneLocation(ctx, tx, wid, "returns")
	if err != nil {
		return rc, err
	}

	rc.RAID = raID
	for i, l := range lines {
		lot, err := s.resolveLot(ctx, tx, wid, orderID, l)
		if err != nil {
			return rc, err
		}
		// No more can come back than went out.
		var shipped, returned int64
		if err := tx.QueryRowContext(ctx, `
			SELECT COALESCE((SELECT SUM(qty) FROM allocations WHERE warehouse_id=$1 AND order_id=$2 AND sku_id=$3 AND state='shipped'),0),
			       COALESCE((SELECT SUM(qty) FROM return_lines WHERE warehouse_id=$1 AND ra_id=$4 AND sku_id=$3),0)`,
			string(wid), orderID, l.SKU, raID).Scan(&shipped, &returned); err != nil {
			return rc, err
		}
		if returned+l.Qty > shipped {
			return rc, apperr.Invalid("%s: %d shipped, %d already returned, cannot receive %d more", l.SKU, shipped, returned, l.Qty)
		}
		lineID := ids.New("rl")
		toState := conditionState(l.Condition)
		if err := s.store.AppendMovementTx(ctx, tx, inventory.Movement{
			ID: ids.New("mvt"), WarehouseID: wid, Kind: inventory.KindReturnReceive,
			FromLocation: inventory.VirtualReturnIntake, FromState: inventory.StateAvailable,
			ToLocation: loc, ToState: toState,
			SKUID: inventory.SKUID(l.SKU), LotID: inventory.LotID(lot), Qty: l.Qty, OccurredAt: s.now(),
			IdempotencyKey: fmt.Sprintf("return-%s-%d", key, i), ReturnID: raID, OrderID: orderID, ShipmentID: shipmentID,
		}); err != nil {
			return rc, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO return_lines (id, warehouse_id, ra_id, sku_id, lot_id, qty, condition, location, intake_key, received_by, received_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			lineID, string(wid), raID, l.SKU, lot, l.Qty, l.Condition, string(loc), key, operator, s.now()); err != nil {
			return rc, err
		}
		t, _, err := tasks.Create(ctx, tx, tasks.Task{
			WarehouseID: wid, Kind: inventory.TaskDisposition, SKU: inventory.SKUID(l.SKU), Lot: inventory.LotID(lot),
			FromLocation: loc, Qty: l.Qty, OrderID: orderID, ReceiptID: raID, Ref: lineID, Reason: suggested(l.Condition),
		})
		if err != nil {
			return rc, err
		}
		rc.Lines, rc.Tasks = append(rc.Lines, lineID), append(rc.Tasks, t.ID)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE return_authorisations SET state='received', received_at=COALESCE(received_at,$3) WHERE warehouse_id=$1 AND id=$2 AND state='authorised'`,
		string(wid), raID, s.now()); err != nil {
		return rc, err
	}
	// The expected-parcel task is done.
	var intakeID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE warehouse_id=$1 AND kind='return_intake' AND ref=$2`, string(wid), raID).Scan(&intakeID); err == nil {
		if t, err := tasks.Lock(ctx, tx, wid, intakeID); err == nil && tasks.CanTransition(t.State, inventory.TaskCompleted) {
			if err := tasks.Transition(ctx, tx, t, inventory.TaskCompleted, "", ""); err != nil {
				return rc, err
			}
		}
	}
	if err := outbox.Emit(ctx, tx, string(wid), "return.received", shipmentID, map[string]any{
		"return_authorisation_id": raID, "shipment_id": shipmentID, "lines": len(lines),
	}); err != nil {
		return rc, err
	}
	return rc, tx.Commit()
}

// suggested is the default decision shown to the operator.
func suggested(condition string) string {
	switch condition {
	case Good:
		return Restock
	case Damaged:
		return Scrap
	}
	return Quarantine
}

// conditionState is the stock state a returned unit sits in until it is decided.
func conditionState(condition string) inventory.StockState {
	switch condition {
	case Good:
		return inventory.StateAvailable
	case Damaged:
		return inventory.StateDamaged
	}
	return inventory.StateQuarantined
}

// resolveLot uses the lot that was shipped for this order and SKU unless the
// operator gave one, and falls back to a placeholder lot.
func (s *Service) resolveLot(ctx context.Context, tx *sql.Tx, wid inventory.WarehouseID, orderID string, l IntakeLine) (string, error) {
	lot := l.Lot
	if lot == "" {
		err := tx.QueryRowContext(ctx, `SELECT lot_id FROM allocations WHERE warehouse_id=$1 AND order_id=$2 AND sku_id=$3 AND state='shipped' ORDER BY id LIMIT 1`,
			string(wid), orderID, l.SKU).Scan(&lot)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	if lot == "" {
		lot = "LOT-RETURN-" + l.SKU
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO lots (id, sku_id, warehouse_id, received_at)
		SELECT $1, $2, $3, NOW() WHERE EXISTS (SELECT 1 FROM skus WHERE warehouse_id=$3 AND id=$2)
		ON CONFLICT (warehouse_id, id) DO NOTHING`, lot, l.SKU, string(wid))
	return lot, err
}

func zoneLocation(ctx context.Context, tx *sql.Tx, wid inventory.WarehouseID, purpose string) (inventory.LocationCode, error) {
	var code string
	err := tx.QueryRowContext(ctx, `
		SELECT l.code FROM locations l JOIN zones z ON z.id=l.zone_id
		WHERE l.warehouse_id=$1 AND z.purpose=$2 ORDER BY l.code LIMIT 1`, string(wid), purpose).Scan(&code)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w (%s)", ErrNoLocation, purpose)
	}
	return inventory.LocationCode(code), err
}

// Disposition decides a returned line: restock it, quarantine it for
// inspection, or scrap it. The movement and the task commit together. Repeating
// it is a no-op. destination is optional for a restock; the putaway rule picks
// one when it is empty.
func (s *Service) Disposition(ctx context.Context, wid inventory.WarehouseID, taskID, decision, destination string) error {
	if decision != Restock && decision != Quarantine && decision != Scrap {
		return apperr.Invalid("decision must be restock, quarantine or scrap")
	}
	return tasks.Do(ctx, s.db, wid, taskID, func(tx *sql.Tx, t tasks.Task) error {
		if t.Kind != inventory.TaskDisposition {
			return ErrWrongTask
		}
		if t.State == inventory.TaskCompleted {
			return nil
		}
		var condition, raID string
		if err := tx.QueryRowContext(ctx, `SELECT condition, ra_id FROM return_lines WHERE warehouse_id=$1 AND id=$2 FOR UPDATE`, string(wid), t.Ref).Scan(&condition, &raID); err != nil {
			return err
		}
		from := conditionState(condition)
		var to inventory.LocationCode
		toState := from
		switch decision {
		case Restock:
			if condition != Good {
				return apperr.Invalid("only units marked good can be restocked; quarantine or scrap this one")
			}
			to = inventory.LocationCode(destination)
			if to == "" {
				var err error
				if to, err = inbound.ChooseDestination(ctx, tx, wid, t.SKU, t.Qty); err != nil {
					return err
				}
			}
			if to == "" {
				return apperr.Invalid("no storage location has room; scan a destination or quarantine the unit")
			}
			toState = inventory.StateAvailable
		case Quarantine:
			var err error
			if to, err = zoneLocation(ctx, tx, wid, "quarantine"); err != nil {
				return err
			}
			toState = inventory.StateQuarantined
		case Scrap:
			to, toState = inventory.VirtualScrap, inventory.StateDamaged
		}
		if err := s.store.AppendMovementTx(ctx, tx, inventory.Movement{
			ID: ids.New("mvt"), WarehouseID: wid, Kind: inventory.KindDisposition,
			FromLocation: t.FromLocation, FromState: from, ToLocation: to, ToState: toState,
			SKUID: t.SKU, LotID: t.Lot, Qty: t.Qty, OccurredAt: s.now(),
			IdempotencyKey: "task-" + t.ID, TaskID: t.ID, ReturnID: raID, OrderID: t.OrderID,
		}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE return_lines SET state='dispositioned', decision=$3, destination=$4, dispositioned_at=$5
			WHERE warehouse_id=$1 AND id=$2`, string(wid), t.Ref, decision, string(to), s.now()); err != nil {
			return err
		}
		if err := tasks.Transition(ctx, tx, t, inventory.TaskCompleted, "", ""); err != nil {
			return err
		}
		var open int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM return_lines WHERE warehouse_id=$1 AND ra_id=$2 AND state<>'dispositioned'`, string(wid), raID).Scan(&open); err != nil {
			return err
		}
		if open == 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE return_authorisations SET state='dispositioned' WHERE warehouse_id=$1 AND id=$2`, string(wid), raID); err != nil {
				return err
			}
		}
		return outbox.Emit(ctx, tx, string(wid), "return.dispositioned", t.Ref, map[string]any{
			"return_authorisation_id": raID, "return_line_id": t.Ref, "decision": decision, "destination": string(to), "sku": string(t.SKU), "qty": t.Qty,
		})
	})
}

// RA is a return authorisation with its lines.
type RA struct {
	ID, ShipmentID, OrderID, Reason, State, Tracking, Carrier string
	CreatedAt                                                  time.Time
	ReceivedAt                                                 *time.Time
	Lines                                                      []Line
}

type Line struct {
	ID, SKU, Lot, Condition, Location, State, Decision, Destination string
	Qty                                                              int64
}

// List pages return authorisations newest first. state "" is all.
func (s *Service) List(ctx context.Context, wid inventory.WarehouseID, state string, afterCreated time.Time, afterID string, limit int) ([]RA, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if afterID == "" {
		afterCreated = time.Now().Add(100 * 365 * 24 * time.Hour)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.shipment_id, r.order_id, r.reason, r.state, sh.tracking, sh.carrier, r.created_at, r.received_at
		FROM return_authorisations r JOIN shipments sh ON sh.warehouse_id=r.warehouse_id AND sh.id=r.shipment_id
		WHERE r.warehouse_id=$1 AND ($2='' OR r.state=$2) AND (r.created_at, r.id) < ($3, $4)
		ORDER BY r.created_at DESC, r.id DESC LIMIT $5`, string(wid), state, afterCreated, afterIDOrMax(afterID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RA
	for rows.Next() {
		var r RA
		if err := rows.Scan(&r.ID, &r.ShipmentID, &r.OrderID, &r.Reason, &r.State, &r.Tracking, &r.Carrier, &r.CreatedAt, &r.ReceivedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Lines, err = s.lines(ctx, wid, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func afterIDOrMax(id string) string {
	if id == "" {
		return "\U0010FFFF"
	}
	return id
}

func (s *Service) lines(ctx context.Context, wid inventory.WarehouseID, raID string) ([]Line, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, sku_id, lot_id, qty, condition, location, state, decision, destination
		FROM return_lines WHERE warehouse_id=$1 AND ra_id=$2 ORDER BY id`, string(wid), raID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Line
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.ID, &l.SKU, &l.Lot, &l.Qty, &l.Condition, &l.Location, &l.State, &l.Decision, &l.Destination); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ByTracking finds the return expected for a tracking number (scanner lookup).
func (s *Service) ByTracking(ctx context.Context, wid inventory.WarehouseID, tracking string) (RA, error) {
	var r RA
	err := s.db.QueryRowContext(ctx, `
		SELECT r.id, r.shipment_id, r.order_id, r.reason, r.state, sh.tracking, sh.carrier, r.created_at, r.received_at
		FROM return_authorisations r JOIN shipments sh ON sh.warehouse_id=r.warehouse_id AND sh.id=r.shipment_id
		WHERE r.warehouse_id=$1 AND sh.tracking=$2`, string(wid), tracking).
		Scan(&r.ID, &r.ShipmentID, &r.OrderID, &r.Reason, &r.State, &r.Tracking, &r.Carrier, &r.CreatedAt, &r.ReceivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.Lines, err = s.lines(ctx, wid, r.ID)
	return r, err
}
