// Package inbound implements ASN, receipt, and putaway (spec 3.4).
package inbound

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbox"
	"github.com/nicholasgasior/crossdock/internal/tasks"
)

var (
	ErrNotFound     = errors.New("inbound: not found")
	ErrLocationFull = errors.New("inbound: destination location is full")
	ErrWrongTask    = errors.New("inbound: task is not a putaway task")
)

// Service holds inbound operations.
type Service struct {
	db    *sql.DB
	store *inventory.Store
}

func NewService(db *sql.DB, store *inventory.Store) *Service {
	return &Service{db: db, store: store}
}

// ASNLine is what the supplier says is coming.
type ASNLine struct {
	ID          string
	SKU         inventory.SKUID
	Lot         inventory.LotID // optional
	ExpectedQty int64
	ReceivedQty int64
	Short       bool
}

// ASN is an advance shipping notice.
type ASN struct {
	ID          string
	WarehouseID inventory.WarehouseID
	Ref         string
	Supplier    string
	State       string
	Lines       []ASNLine
}

// CreateASN records an ASN. It is idempotent on (warehouse, ref): a repeat
// returns the existing ASN unchanged.
func (s *Service) CreateASN(ctx context.Context, wid inventory.WarehouseID, ref, supplier string, lines []ASNLine) (ASN, error) {
	if ref == "" || len(lines) == 0 {
		return ASN{}, apperr.Invalid("ref and at least one line are required")
	}
	for _, l := range lines {
		if l.SKU == "" || l.ExpectedQty <= 0 {
			return ASN{}, apperr.Invalid("each line needs a sku and a positive expected_qty")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ASN{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	id := ids.New("asn")
	var got string
	err = tx.QueryRowContext(ctx, `
		INSERT INTO asns (id, warehouse_id, ref, supplier) VALUES ($1,$2,$3,$4)
		ON CONFLICT (warehouse_id, ref) DO NOTHING RETURNING id`,
		id, string(wid), ref, supplier).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		tx.Rollback() //nolint:errcheck
		return s.GetASNByRef(ctx, wid, ref)
	}
	if err != nil {
		return ASN{}, err
	}
	for _, l := range lines {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO asn_lines (id, warehouse_id, asn_id, sku_id, lot_id, expected_qty)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			ids.New("asl"), string(wid), id, string(l.SKU), string(l.Lot), l.ExpectedQty); err != nil {
			return ASN{}, fmt.Errorf("inbound: insert line (sku %q): %w", l.SKU, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return ASN{}, err
	}
	return s.GetASN(ctx, wid, id)
}

func (s *Service) GetASNByRef(ctx context.Context, wid inventory.WarehouseID, ref string) (ASN, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM asns WHERE warehouse_id=$1 AND ref=$2`, string(wid), ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ASN{}, ErrNotFound
	}
	if err != nil {
		return ASN{}, err
	}
	return s.GetASN(ctx, wid, id)
}

func (s *Service) GetASN(ctx context.Context, wid inventory.WarehouseID, id string) (ASN, error) {
	a := ASN{WarehouseID: wid}
	err := s.db.QueryRowContext(ctx, `SELECT id, ref, supplier, state FROM asns WHERE warehouse_id=$1 AND id=$2`,
		string(wid), id).Scan(&a.ID, &a.Ref, &a.Supplier, &a.State)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, sku_id, lot_id, expected_qty, received_qty, short
		FROM asn_lines WHERE warehouse_id=$1 AND asn_id=$2 ORDER BY id`, string(wid), id)
	if err != nil {
		return a, err
	}
	defer rows.Close()
	for rows.Next() {
		var l ASNLine
		if err := rows.Scan(&l.ID, (*string)(&l.SKU), (*string)(&l.Lot), &l.ExpectedQty, &l.ReceivedQty, &l.Short); err != nil {
			return a, err
		}
		a.Lines = append(a.Lines, l)
	}
	return a, rows.Err()
}

// ReceiveInput is one scanned receipt line.
type ReceiveInput struct {
	WarehouseID inventory.WarehouseID
	ASNLineID   string
	Qty         int64
	LPN         inventory.LPNID
	Location    inventory.LocationCode // receiving dock location
	Lot         inventory.LotID        // overrides the ASN line lot when set
	ExpiresAt   *time.Time
	Key         string // idempotency key (client generated)
}

// Receipt is the result of a receive scan.
type Receipt struct {
	ID         string
	PutawayID  string
	ToLocation inventory.LocationCode
	Duplicate  bool
}

// Receive scans stock off the dock: a receive movement SUPPLIER -> dock, a
// receipt row, and a putaway task, all in one transaction. A repeat of the same
// Key is a no-op that returns the original result.
func (s *Service) Receive(ctx context.Context, in ReceiveInput) (Receipt, error) {
	if in.Key == "" || in.Qty <= 0 || in.ASNLineID == "" {
		return Receipt{}, apperr.Invalid("key, asn_line_id and a positive qty are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	// Idempotency: the unique (warehouse, key) on receipts also guards races;
	// this read handles the common sequential retry.
	var prev Receipt
	err = tx.QueryRowContext(ctx, `
		SELECT r.id, COALESCE(t.id,''), COALESCE(t.to_location,'')
		FROM receipts r LEFT JOIN tasks t ON t.warehouse_id=r.warehouse_id AND t.kind='putaway' AND t.ref=r.id
		WHERE r.warehouse_id=$1 AND r.idempotency_key=$2`, string(in.WarehouseID), in.Key,
	).Scan(&prev.ID, &prev.PutawayID, (*string)(&prev.ToLocation))
	if err == nil {
		prev.Duplicate = true
		return prev, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, err
	}

	var (
		asnID, lotOnLine string
		sku              inventory.SKUID
	)
	err = tx.QueryRowContext(ctx, `
		SELECT asn_id, sku_id, lot_id FROM asn_lines WHERE warehouse_id=$1 AND id=$2 FOR UPDATE`,
		string(in.WarehouseID), in.ASNLineID).Scan(&asnID, (*string)(&sku), &lotOnLine)
	if errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, ErrNotFound
	}
	if err != nil {
		return Receipt{}, err
	}

	lot := in.Lot
	if lot == "" {
		lot = inventory.LotID(lotOnLine)
	}
	if lot == "" {
		lot = inventory.LotID("LOT-" + asnID + "-" + string(sku))
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO lots (id, sku_id, warehouse_id, received_at, expires_at) VALUES ($1,$2,$3,NOW(),$4)
		ON CONFLICT (warehouse_id, id) DO NOTHING`,
		string(lot), string(sku), string(in.WarehouseID), in.ExpiresAt); err != nil {
		return Receipt{}, fmt.Errorf("inbound: ensure lot: %w", err)
	}

	rid := ids.New("rcp")
	mv := inventory.Movement{
		ID: ids.New("mvt"), WarehouseID: in.WarehouseID, Kind: inventory.KindReceive,
		FromLocation: inventory.VirtualSupplier, FromState: inventory.StateAvailable,
		ToLocation: in.Location, ToState: inventory.StateAvailable,
		SKUID: sku, LotID: lot, LPNID: in.LPN, Qty: in.Qty, OccurredAt: time.Now(),
		IdempotencyKey: "receive-" + in.Key, ReceiptID: rid,
	}
	if err := s.store.AppendMovementTx(ctx, tx, mv); err != nil {
		return Receipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO receipts (id, warehouse_id, asn_id, asn_line_id, sku_id, lot_id, qty, lpn_id, location, idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		rid, string(in.WarehouseID), asnID, in.ASNLineID, string(sku), string(lot), in.Qty,
		string(in.LPN), string(in.Location), in.Key); err != nil {
		return Receipt{}, fmt.Errorf("inbound: insert receipt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE asn_lines SET received_qty = received_qty + $3 WHERE warehouse_id=$1 AND id=$2`,
		string(in.WarehouseID), in.ASNLineID, in.Qty); err != nil {
		return Receipt{}, err
	}

	dest, err := chooseDestination(ctx, tx, in.WarehouseID, sku, in.Qty)
	if err != nil {
		return Receipt{}, err
	}
	t, _, err := tasks.Create(ctx, tx, tasks.Task{
		WarehouseID: in.WarehouseID, Kind: inventory.TaskPutaway, SKU: sku, Lot: lot, LPN: in.LPN,
		FromLocation: in.Location, ToLocation: dest, Qty: in.Qty, ReceiptID: rid, Ref: rid,
	})
	if err != nil {
		return Receipt{}, err
	}
	if dest == "" { // no room anywhere: a human has to decide
		if err := tasks.Transition(ctx, tx, t, inventory.TaskException, "", "no_capacity"); err != nil {
			return Receipt{}, err
		}
	}
	if err := completeASNIfDone(ctx, tx, in.WarehouseID, asnID); err != nil {
		return Receipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return Receipt{}, err
	}
	return Receipt{ID: rid, PutawayID: t.ID, ToLocation: dest}, nil
}

// MarkShort closes an ASN line as short-shipped: no more stock is expected.
func (s *Service) MarkShort(ctx context.Context, wid inventory.WarehouseID, lineID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var asnID string
	err = tx.QueryRowContext(ctx, `
		UPDATE asn_lines SET short = TRUE WHERE warehouse_id=$1 AND id=$2 RETURNING asn_id`,
		string(wid), lineID).Scan(&asnID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := completeASNIfDone(ctx, tx, wid, asnID); err != nil {
		return err
	}
	return tx.Commit()
}

// completeASNIfDone flips the ASN to received (and emits receipt.completed once)
// when every line is fully received or marked short.
func completeASNIfDone(ctx context.Context, tx *sql.Tx, wid inventory.WarehouseID, asnID string) error {
	var open int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM asn_lines
		WHERE warehouse_id=$1 AND asn_id=$2 AND NOT short AND received_qty < expected_qty`,
		string(wid), asnID).Scan(&open); err != nil || open > 0 {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE asns SET state='received' WHERE warehouse_id=$1 AND id=$2 AND state='open'`,
		string(wid), asnID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return outbox.Emit(ctx, tx, string(wid), "receipt.completed", asnID, map[string]any{"asn_id": asnID})
	}
	return nil
}

// chooseDestination picks a storage location with room: prefer a location that
// already holds the SKU (consolidate), then pick-face over bulk, then lowest code.
// It returns "" when nothing has capacity.
func chooseDestination(ctx context.Context, tx *sql.Tx, wid inventory.WarehouseID, sku inventory.SKUID, qty int64) (inventory.LocationCode, error) {
	var code string
	err := tx.QueryRowContext(ctx, `
		SELECT l.code
		FROM locations l
		JOIN zones z ON z.id = l.zone_id
		LEFT JOIN (
			SELECT location,
			       SUM(available+reserved+in_transit+quarantined+damaged) AS on_hand,
			       BOOL_OR(sku_id = $2) AS has_sku
			FROM positions WHERE warehouse_id = $1 GROUP BY location
		) p ON p.location = l.code
		WHERE l.warehouse_id = $1 AND z.purpose IN ('pick_face','bulk')
		  AND COALESCE(p.on_hand,0) + $3 <= l.max_units
		ORDER BY COALESCE(p.has_sku,FALSE) DESC, (z.purpose = 'pick_face') DESC, l.code
		LIMIT 1`, string(wid), string(sku), qty).Scan(&code)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return inventory.LocationCode(code), err
}

// CompletePutaway executes a putaway task: movement dock -> destination, task
// completed, in one transaction (invariant 7). Repeating it is a no-op. If the
// destination filled up since the task was created it returns ErrLocationFull
// and leaves the task untouched; the caller should Raise an exception.
func (s *Service) CompletePutaway(ctx context.Context, wid inventory.WarehouseID, taskID string, dest inventory.LocationCode) error {
	return tasks.Do(ctx, s.db, wid, taskID, func(tx *sql.Tx, t tasks.Task) error {
		if t.Kind != inventory.TaskPutaway {
			return ErrWrongTask
		}
		if t.State == inventory.TaskCompleted {
			return nil
		}
		if dest == "" {
			dest = t.ToLocation
		}
		if dest == "" {
			return apperr.Invalid("putaway has no destination; scan one")
		}
		var maxUnits, onHand int64
		err := tx.QueryRowContext(ctx, `
			SELECT l.max_units, COALESCE((SELECT SUM(available+reserved+in_transit+quarantined+damaged)
				FROM positions p WHERE p.warehouse_id=l.warehouse_id AND p.location=l.code),0)
			FROM locations l WHERE l.warehouse_id=$1 AND l.code=$2`, string(wid), string(dest)).Scan(&maxUnits, &onHand)
		if errors.Is(err, sql.ErrNoRows) {
			return apperr.Invalid("unknown location %q", dest)
		}
		if err != nil {
			return err
		}
		if maxUnits > 0 && onHand+t.Qty > maxUnits {
			return ErrLocationFull
		}
		mv := inventory.Movement{
			ID: ids.New("mvt"), WarehouseID: wid, Kind: inventory.KindPutaway,
			FromLocation: t.FromLocation, FromState: inventory.StateAvailable,
			ToLocation: dest, ToState: inventory.StateAvailable,
			SKUID: t.SKU, LotID: t.Lot, LPNID: t.LPN, Qty: t.Qty, OccurredAt: time.Now(),
			IdempotencyKey: "task-" + t.ID, TaskID: t.ID, ReceiptID: t.ReceiptID,
		}
		if err := s.store.AppendMovementTx(ctx, tx, mv); err != nil {
			return err
		}
		return tasks.Transition(ctx, tx, t, inventory.TaskCompleted, "", "")
	})
}

// ChooseDestination is the putaway rule exported for restocking returns: a
// storage location with room, preferring one that already holds the SKU, then
// pick face over bulk, then lowest code. It returns "" when nothing has room.
func ChooseDestination(ctx context.Context, tx *sql.Tx, wid inventory.WarehouseID, sku inventory.SKUID, qty int64) (inventory.LocationCode, error) {
	return chooseDestination(ctx, tx, wid, sku, qty)
}
