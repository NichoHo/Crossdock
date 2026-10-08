package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/nicholasgasior/crossdock/internal/outbox"
)

// ErrDuplicateMovement is returned when an operation is retried with the same
// idempotency key that was already processed (the idempotent case).
var ErrDuplicateMovement = errors.New("inventory: duplicate movement (idempotency key already processed)")

// ErrMovementConflict is returned when a movement cannot be applied because the
// required stock is not available (e.g. reserve with insufficient available qty).
var ErrMovementConflict = errors.New("inventory: movement conflict: insufficient quantity")

// ErrPositionNotFound is returned when a position row does not exist.
var ErrPositionNotFound = errors.New("inventory: position not found")

// Store is the persistence layer for inventory. It is responsible for:
//   - Appending movements to the log.
//   - Updating positions in the same transaction.
//   - Providing read access to movements and positions.
//   - Supporting the rebuild operation (truncate + replay).
type Store struct {
	db *sql.DB
}

// NewStore creates a Store backed by the given database connection pool.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying *sql.DB. Exposed for migration runners and tests.
func (s *Store) DB() *sql.DB { return s.db }

// ---------------------------------------------------------------------------
// Writing movements
// ---------------------------------------------------------------------------

// AppendMovement appends a movement to the log and updates the affected
// positions in a single database transaction.
//
// It acquires row locks on the affected position rows in a consistent order
// (warehouse_id, location, sku_id, lot_id, lpn_id) to prevent deadlock when
// multiple goroutines operate on the same positions concurrently.
//
// If the movement's IdempotencyKey has already been processed, it returns
// ErrDuplicateMovement. The caller should treat this as a successful no-op.
//
// If the movement would reduce a position below zero for a real location, it
// returns ErrMovementConflict.
func (s *Store) AppendMovement(ctx context.Context, m Movement) error {
	if err := m.Validate(); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("inventory: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if err := s.appendMovementTx(ctx, tx, m); err != nil {
		return err
	}
	return tx.Commit()
}

// AppendMovementTx is like AppendMovement but uses a caller-supplied transaction.
// Use this when you need to combine the movement with other domain writes.
func (s *Store) AppendMovementTx(ctx context.Context, tx *sql.Tx, m Movement) error {
	return s.appendMovementTx(ctx, tx, m)
}

// BeginTx begins a transaction with the given options.
func (s *Store) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, opts)
}

func (s *Store) appendMovementTx(ctx context.Context, tx *sql.Tx, m Movement) error {
	// -----------------------------------------------------------------------
	// 1. Acquire row locks on affected positions in canonical order.
	// -----------------------------------------------------------------------
	fromKey, toKey := m.PositionDelta()

	// Lock order: sort positions by (warehouse_id, location, sku_id, lot_id, lpn_id).
	keys := uniquePositionKeys(fromKey, toKey)
	for _, k := range sortedPositionKeys(keys) {
		if _, err := lockPosition(ctx, tx, k); err != nil && !errors.Is(err, ErrPositionNotFound) {
			return fmt.Errorf("inventory: lock position %v: %w", k, err)
		}
	}

	// -----------------------------------------------------------------------
	// 2. Insert the movement row.
	// -----------------------------------------------------------------------
	_, err := tx.ExecContext(ctx, `
		INSERT INTO movements (
			id, warehouse_id, kind,
			from_location, from_state, to_location, to_state,
			sku_id, lot_id, lpn_id, qty,
			occurred_at, idempotency_key,
			task_id, receipt_id, order_id, count_id, return_id, shipment_id
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		m.ID, m.WarehouseID, string(m.Kind),
		string(m.FromLocation), string(m.FromState),
		string(m.ToLocation), string(m.ToState),
		string(m.SKUID), string(m.LotID), string(m.LPNID), m.Qty,
		m.OccurredAt, m.IdempotencyKey,
		m.TaskID, m.ReceiptID, m.OrderID, m.CountID, m.ReturnID, m.ShipmentID,
	)
	if err != nil {
		var pgErr *pq.Error
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "23505": // unique_violation
				if strings.Contains(pgErr.Constraint, "idempotency") || pgErr.Constraint == "movements_pkey" {
					return ErrDuplicateMovement
				}
			}
		}
		return fmt.Errorf("inventory: insert movement: %w", err)
	}

	// -----------------------------------------------------------------------
	// 3. Update the from-position (debit).
	// -----------------------------------------------------------------------
	if !fromKey.Location.IsVirtual() || fromKey == toKey {
		if err := s.upsertPosition(ctx, tx, fromKey, m.FromState, -m.Qty); err != nil {
			return fmt.Errorf("inventory: debit from position: %w", err)
		}
	} else {
		// Virtual from-location: still track it but allow negatives.
		if err := s.upsertPositionVirtual(ctx, tx, fromKey, m.FromState, -m.Qty); err != nil {
			return fmt.Errorf("inventory: debit virtual from position: %w", err)
		}
	}

	// -----------------------------------------------------------------------
	// 4. Update the to-position (credit).
	// -----------------------------------------------------------------------
	// When from == to (state change within same location), we already applied
	// the debit above; now apply the credit to the to-state.
	if fromKey == toKey {
		if err := s.upsertPosition(ctx, tx, toKey, m.ToState, m.Qty); err != nil {
			return fmt.Errorf("inventory: credit to position (same location): %w", err)
		}
	} else if !toKey.Location.IsVirtual() {
		if err := s.upsertPosition(ctx, tx, toKey, m.ToState, m.Qty); err != nil {
			return fmt.Errorf("inventory: credit to position: %w", err)
		}
	} else {
		if err := s.upsertPositionVirtual(ctx, tx, toKey, m.ToState, m.Qty); err != nil {
			return fmt.Errorf("inventory: credit virtual to position: %w", err)
		}
	}

	// 5. Outbox: the stock.moved event commits with the movement (commitment 5).
	return outbox.Emit(ctx, tx, string(m.WarehouseID), "stock.moved", m.ID, map[string]any{
		"id": m.ID, "kind": m.Kind, "sku": m.SKUID, "lot": m.LotID, "qty": m.Qty,
		"from": m.FromLocation, "from_state": m.FromState,
		"to": m.ToLocation, "to_state": m.ToState,
		"order_id": m.OrderID, "shipment_id": m.ShipmentID, "occurred_at": m.OccurredAt,
	})
}

// upsertPosition performs an INSERT … ON CONFLICT DO UPDATE for a real
// (non-virtual) location's position, applying the given quantity delta to
// the specified state column. It returns ErrMovementConflict if the result
// would be negative.
func (s *Store) upsertPosition(ctx context.Context, tx *sql.Tx, k PositionKey, state StockState, delta int64) error {
	col := stateColumn(state)
	// Ensure the row exists with zeros first: a negative delta in the INSERT
	// tuple would trip the >= 0 CHECK before ON CONFLICT is considered. The
	// UPDATE then applies the delta, and the CHECK rejects any negative result.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO positions (warehouse_id, sku_id, lot_id, location, lpn_id)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING`,
		string(k.WarehouseID), string(k.SKU), string(k.Lot),
		string(k.Location), string(k.LPN),
	); err != nil {
		return fmt.Errorf("inventory: ensure position: %w", err)
	}
	q := fmt.Sprintf(`
		UPDATE positions SET %s = %s + $6, updated_at = NOW()
		WHERE warehouse_id=$1 AND sku_id=$2 AND lot_id=$3 AND location=$4 AND lpn_id=$5
		RETURNING %s`, col, col, col)

	var result int64
	err := tx.QueryRowContext(ctx, q,
		string(k.WarehouseID), string(k.SKU), string(k.Lot),
		string(k.Location), string(k.LPN), delta,
	).Scan(&result)
	if err != nil {
		var pgErr *pq.Error
		if errors.As(err, &pgErr) && pgErr.Code == "23514" { // check_violation
			return ErrMovementConflict
		}
		return fmt.Errorf("inventory: upsert position: %w", err)
	}
	if result < 0 {
		return ErrMovementConflict
	}
	return nil
}

// upsertPositionVirtual is like upsertPosition but does not check for negative
// results (virtual locations can go negative — that is how conservation works).
func (s *Store) upsertPositionVirtual(ctx context.Context, tx *sql.Tx, k PositionKey, state StockState, delta int64) error {
	col := stateColumn(state)
	q := fmt.Sprintf(`
		INSERT INTO positions (warehouse_id, sku_id, lot_id, location, lpn_id, %s, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (warehouse_id, sku_id, lot_id, location, lpn_id)
		DO UPDATE SET %s = positions.%s + $6, updated_at = NOW()`, col, col, col)
	_, err := tx.ExecContext(ctx, q,
		string(k.WarehouseID), string(k.SKU), string(k.Lot),
		string(k.Location), string(k.LPN), delta,
	)
	if err != nil {
		return fmt.Errorf("inventory: upsert virtual position: %w", err)
	}
	return nil
}

// stateColumn maps a StockState to the positions table column name.
func stateColumn(s StockState) string {
	switch s {
	case StateAvailable:
		return "available"
	case StateReserved:
		return "reserved"
	case StateInTransit:
		return "in_transit"
	case StateQuarantined:
		return "quarantined"
	case StateDamaged:
		return "damaged"
	default:
		panic(fmt.Sprintf("inventory: unknown state %q", s))
	}
}

// lockPosition attempts to acquire a row-level lock on the position identified
// by key. If the row does not exist it returns ErrPositionNotFound (no lock
// needed since no row = no conflict). The caller should continue and let the
// upsert create the row.
func lockPosition(ctx context.Context, tx *sql.Tx, k PositionKey) (*Position, error) {
	var p Position
	err := tx.QueryRowContext(ctx, `
		SELECT warehouse_id, sku_id, lot_id, location, lpn_id,
		       available, reserved, in_transit, quarantined, damaged
		FROM positions
		WHERE warehouse_id=$1 AND sku_id=$2 AND lot_id=$3 AND location=$4 AND lpn_id=$5
		FOR UPDATE`,
		string(k.WarehouseID), string(k.SKU), string(k.Lot),
		string(k.Location), string(k.LPN),
	).Scan(
		(*string)(&p.WarehouseID), (*string)(&p.SKU), (*string)(&p.Lot),
		(*string)(&p.Location), (*string)(&p.LPN),
		&p.Available, &p.Reserved, &p.InTransit, &p.Quarantined, &p.Damaged,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPositionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ---------------------------------------------------------------------------
// Reading movements
// ---------------------------------------------------------------------------

// ListMovements returns movements for a warehouse in seq order, starting after
// afterSeq (exclusive). Limit controls the page size; 0 means no limit (use
// with care on large logs).
func (s *Store) ListMovements(ctx context.Context, warehouseID WarehouseID, afterSeq int64, limit int) ([]Movement, error) {
	q := `
		SELECT id, warehouse_id, seq, kind,
		       from_location, from_state, to_location, to_state,
		       sku_id, lot_id, lpn_id, qty, occurred_at, created_at,
		       idempotency_key, task_id, receipt_id, order_id, count_id, return_id, shipment_id
		FROM movements
		WHERE warehouse_id = $1 AND seq > $2
		ORDER BY seq ASC`
	args := []any{string(warehouseID), afterSeq}
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("inventory: list movements: %w", err)
	}
	defer rows.Close()

	var out []Movement
	for rows.Next() {
		var m Movement
		if err := scanMovement(rows, &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// scanMovement scans a movement row from a *sql.Rows.
func scanMovement(rows *sql.Rows, m *Movement) error {
	return rows.Scan(
		&m.ID, (*string)(&m.WarehouseID), &m.Seq, (*string)(&m.Kind),
		(*string)(&m.FromLocation), (*string)(&m.FromState),
		(*string)(&m.ToLocation), (*string)(&m.ToState),
		(*string)(&m.SKUID), (*string)(&m.LotID), (*string)(&m.LPNID),
		&m.Qty, &m.OccurredAt, &m.CreatedAt,
		&m.IdempotencyKey, &m.TaskID, &m.ReceiptID, &m.OrderID,
		&m.CountID, &m.ReturnID, &m.ShipmentID,
	)
}

// ---------------------------------------------------------------------------
// Reading positions
// ---------------------------------------------------------------------------

// GetPosition returns the current position for the given key. Returns
// ErrPositionNotFound if no stock has ever been at this position.
func (s *Store) GetPosition(ctx context.Context, k PositionKey) (*Position, error) {
	return s.getPositionFromDB(ctx, s.db, k)
}

func (s *Store) getPositionFromDB(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, k PositionKey) (*Position, error) {
	var p Position
	err := q.QueryRowContext(ctx, `
		SELECT warehouse_id, sku_id, lot_id, location, lpn_id,
		       available, reserved, in_transit, quarantined, damaged
		FROM positions
		WHERE warehouse_id=$1 AND sku_id=$2 AND lot_id=$3 AND location=$4 AND lpn_id=$5`,
		string(k.WarehouseID), string(k.SKU), string(k.Lot),
		string(k.Location), string(k.LPN),
	).Scan(
		(*string)(&p.WarehouseID), (*string)(&p.SKU), (*string)(&p.Lot),
		(*string)(&p.Location), (*string)(&p.LPN),
		&p.Available, &p.Reserved, &p.InTransit, &p.Quarantined, &p.Damaged,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPositionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("inventory: get position: %w", err)
	}
	return &p, nil
}

// AllPositions returns every position row for a warehouse.
func (s *Store) AllPositions(ctx context.Context, warehouseID WarehouseID) ([]Position, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT warehouse_id, sku_id, lot_id, location, lpn_id,
		       available, reserved, in_transit, quarantined, damaged
		FROM positions
		WHERE warehouse_id=$1
		ORDER BY location, sku_id, lot_id, lpn_id`,
		string(warehouseID),
	)
	if err != nil {
		return nil, fmt.Errorf("inventory: all positions: %w", err)
	}
	defer rows.Close()
	return scanPositions(rows)
}

func scanPositions(rows *sql.Rows) ([]Position, error) {
	var out []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(
			(*string)(&p.WarehouseID), (*string)(&p.SKU), (*string)(&p.Lot),
			(*string)(&p.Location), (*string)(&p.LPN),
			&p.Available, &p.Reserved, &p.InTransit, &p.Quarantined, &p.Damaged,
		); err != nil {
			return nil, fmt.Errorf("inventory: scan position: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Rebuild positions (projection replay)
// ---------------------------------------------------------------------------

// RebuildPositions truncates the positions table and replays every movement
// in the log to rebuild it from scratch. This operation is:
//   - Wrapped in a single transaction (all-or-nothing).
//   - Used by `crossdock rebuild positions` and the projection fidelity test.
//
// The rebuild acquires an ACCESS EXCLUSIVE lock on the positions table to
// prevent reads during the rebuild (which would see a partially-rebuilt state).
// In production this should be run during a maintenance window or with a
// replica serving reads.
func (s *Store) RebuildPositions(ctx context.Context, warehouseID WarehouseID) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("inventory: rebuild positions: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Lock positions table exclusively for the duration of the rebuild.
	if _, err := tx.ExecContext(ctx, `LOCK TABLE positions IN ACCESS EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("inventory: rebuild positions: lock table: %w", err)
	}

	// Truncate positions for this warehouse only.
	if _, err := tx.ExecContext(ctx, `DELETE FROM positions WHERE warehouse_id=$1`, string(warehouseID)); err != nil {
		return fmt.Errorf("inventory: rebuild positions: truncate: %w", err)
	}

	// Replay movements in seq order, accumulating state into an in-memory map,
	// then bulk-insert at the end. This is far faster than doing one upsert per
	// movement for large logs.
	posMap := make(map[PositionKey]*Position)

	// Stream movements in chunks to avoid loading the entire log into memory.
	const chunkSize = 5000
	var afterSeq int64
	for {
		rows, err := tx.QueryContext(ctx, `
			SELECT warehouse_id, sku_id, lot_id, lpn_id,
			       from_location, from_state, to_location, to_state, qty, seq
			FROM movements
			WHERE warehouse_id=$1 AND seq > $2
			ORDER BY seq ASC
			LIMIT $3`,
			string(warehouseID), afterSeq, chunkSize,
		)
		if err != nil {
			return fmt.Errorf("inventory: rebuild positions: query movements: %w", err)
		}

		count := 0
		for rows.Next() {
			var (
				wid                              string
				skuID, lotID, lpnID              string
				fromLoc, fromState               string
				toLoc, toState                   string
				qty                              int64
				seq                              int64
			)
			if err := rows.Scan(&wid, &skuID, &lotID, &lpnID,
				&fromLoc, &fromState, &toLoc, &toState, &qty, &seq); err != nil {
				rows.Close()
				return fmt.Errorf("inventory: rebuild positions: scan: %w", err)
			}
			afterSeq = seq
			count++

			// Apply debit to from-position.
			fromKey := PositionKey{
				WarehouseID: WarehouseID(wid), SKU: SKUID(skuID),
				Lot: LotID(lotID), Location: LocationCode(fromLoc), LPN: LPNID(lpnID),
			}
			applyDelta(posMap, fromKey, StockState(fromState), -qty)

			// Apply credit to to-position.
			toKey := PositionKey{
				WarehouseID: WarehouseID(wid), SKU: SKUID(skuID),
				Lot: LotID(lotID), Location: LocationCode(toLoc), LPN: LPNID(lpnID),
			}
			applyDelta(posMap, toKey, StockState(toState), qty)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("inventory: rebuild positions: rows error: %w", err)
		}
		if count < chunkSize {
			break // last page
		}
	}

	// Bulk insert the rebuilt positions.
	if len(posMap) > 0 {
		if err := bulkInsertPositions(ctx, tx, posMap); err != nil {
			return fmt.Errorf("inventory: rebuild positions: bulk insert: %w", err)
		}
	}

	return tx.Commit()
}

// applyDelta applies a quantity delta to a state column of a position in the map.
func applyDelta(posMap map[PositionKey]*Position, k PositionKey, state StockState, delta int64) {
	p, ok := posMap[k]
	if !ok {
		p = &Position{PositionKey: k}
		posMap[k] = p
	}
	switch state {
	case StateAvailable:
		p.Available += delta
	case StateReserved:
		p.Reserved += delta
	case StateInTransit:
		p.InTransit += delta
	case StateQuarantined:
		p.Quarantined += delta
	case StateDamaged:
		p.Damaged += delta
	}
}

// bulkInsertPositions inserts all positions from the map in one statement using
// unnest arrays for performance.
func bulkInsertPositions(ctx context.Context, tx *sql.Tx, posMap map[PositionKey]*Position) error {
	warehouseIDs := make([]string, 0, len(posMap))
	skuIDs := make([]string, 0, len(posMap))
	lotIDs := make([]string, 0, len(posMap))
	locations := make([]string, 0, len(posMap))
	lpnIDs := make([]string, 0, len(posMap))
	available := make([]int64, 0, len(posMap))
	reserved := make([]int64, 0, len(posMap))
	inTransit := make([]int64, 0, len(posMap))
	quarantined := make([]int64, 0, len(posMap))
	damaged := make([]int64, 0, len(posMap))
	now := time.Now()
	times := make([]time.Time, 0, len(posMap))

	for _, p := range posMap {
		warehouseIDs = append(warehouseIDs, string(p.WarehouseID))
		skuIDs = append(skuIDs, string(p.SKU))
		lotIDs = append(lotIDs, string(p.Lot))
		locations = append(locations, string(p.Location))
		lpnIDs = append(lpnIDs, string(p.LPN))
		available = append(available, p.Available)
		reserved = append(reserved, p.Reserved)
		inTransit = append(inTransit, p.InTransit)
		quarantined = append(quarantined, p.Quarantined)
		damaged = append(damaged, p.Damaged)
		times = append(times, now)
	}

	_, err := tx.ExecContext(ctx, `
		INSERT INTO positions (
			warehouse_id, sku_id, lot_id, location, lpn_id,
			available, reserved, in_transit, quarantined, damaged, updated_at
		)
		SELECT * FROM unnest(
			$1::text[], $2::text[], $3::text[], $4::text[], $5::text[],
			$6::bigint[], $7::bigint[], $8::bigint[], $9::bigint[], $10::bigint[],
			$11::timestamptz[]
		)`,
		pq.Array(warehouseIDs),
		pq.Array(skuIDs),
		pq.Array(lotIDs),
		pq.Array(locations),
		pq.Array(lpnIDs),
		pq.Array(available),
		pq.Array(reserved),
		pq.Array(inTransit),
		pq.Array(quarantined),
		pq.Array(damaged),
		pq.Array(times),
	)
	return err
}

// ---------------------------------------------------------------------------
// Position key helpers
// ---------------------------------------------------------------------------

// uniquePositionKeys returns the set of distinct keys from the provided keys.
// When from == to (state change within same location), we must only lock once.
func uniquePositionKeys(keys ...PositionKey) []PositionKey {
	seen := make(map[PositionKey]struct{})
	var out []PositionKey
	for _, k := range keys {
		if _, ok := seen[k]; !ok {
			seen[k] = struct{}{}
			out = append(out, k)
		}
	}
	return out
}

// sortedPositionKeys returns keys in a canonical order to prevent deadlocks
// when multiple goroutines lock the same positions in different orders.
// Order: warehouse_id, location, sku_id, lot_id, lpn_id (all lexicographic).
func sortedPositionKeys(keys []PositionKey) []PositionKey {
	sorted := make([]PositionKey, len(keys))
	copy(sorted, keys)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if positionKeyLess(sorted[j], sorted[i]) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	return sorted
}

func positionKeyLess(a, b PositionKey) bool {
	if a.WarehouseID != b.WarehouseID {
		return string(a.WarehouseID) < string(b.WarehouseID)
	}
	if a.Location != b.Location {
		return string(a.Location) < string(b.Location)
	}
	if a.SKU != b.SKU {
		return string(a.SKU) < string(b.SKU)
	}
	if a.Lot != b.Lot {
		return string(a.Lot) < string(b.Lot)
	}
	return string(a.LPN) < string(b.LPN)
}
