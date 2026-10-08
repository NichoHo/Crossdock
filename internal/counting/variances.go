package counting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbox"
)

// Variance is a difference between the system and a count, awaiting a decision.
type Variance struct {
	ID                 string
	CountID            string
	Location           string
	SKU, Lot, LPN      string
	SystemQty          int64
	CountedQty         int64
	DamagedQty         int64
	Variance           int64
	InFlight           json.RawMessage
	Proposed           Class
	Classification     Class
	State              string
	Flagged            bool
	RecountTaskID      string
	OpenedAt           time.Time
	ClosedAt           *time.Time
	ConfirmedBy        string
	Note               string
	AdjustmentMovement string
}

const varianceCols = `id, count_id, location, sku_id, lot_id, lpn_id, system_qty, counted_qty, damaged_qty, variance, in_flight,
	proposed, classification, state, flagged, recount_task_id, opened_at, closed_at, confirmed_by, note, adjustment_movement_id`

func scanVariance(r interface{ Scan(...any) error }) (Variance, error) {
	var v Variance
	var p, c string
	err := r.Scan(&v.ID, &v.CountID, &v.Location, &v.SKU, &v.Lot, &v.LPN, &v.SystemQty, &v.CountedQty, &v.DamagedQty, &v.Variance,
		&v.InFlight, &p, &c, &v.State, &v.Flagged, &v.RecountTaskID, &v.OpenedAt, &v.ClosedAt, &v.ConfirmedBy, &v.Note, &v.AdjustmentMovement)
	v.Proposed, v.Classification = Class(p), Class(c)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

func (s *Service) GetVariance(ctx context.Context, wid inventory.WarehouseID, id string) (Variance, error) {
	return scanVariance(s.db.QueryRowContext(ctx, `SELECT `+varianceCols+` FROM variances WHERE warehouse_id=$1 AND id=$2`, string(wid), id))
}

// ListVariances pages variances oldest first. state "" lists everything; "unclosed"
// lists awaiting_recount and open (the workbench default).
func (s *Service) ListVariances(ctx context.Context, wid inventory.WarehouseID, state string, afterOpened time.Time, afterID string, limit int) ([]Variance, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+varianceCols+` FROM variances
		WHERE warehouse_id=$1
		  AND ($2='' OR ($2='unclosed' AND state<>'closed') OR state=$2)
		  AND ($4='' OR (opened_at, id) > ($3, $4))
		ORDER BY opened_at, id LIMIT $5`, string(wid), state, afterOpened, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Variance
	for rows.Next() {
		v, err := scanVariance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Confirm records a supervisor's decision and carries out its action (spec 8.2):
//
//	timing, miscount  close; no stock movement
//	damaged           a quarantine movement, not an adjustment
//	shrinkage         count_adjust to VARIANCE, flagged for review
//	found             count_adjust from VARIANCE
//	unresolved        stays open and keeps ageing; the note is saved
//
// An adjustment cannot post without a classification. Choosing something other
// than what the rules proposed needs a note.
func (s *Service) Confirm(ctx context.Context, wid inventory.WarehouseID, id string, class Class, note, actor string) error {
	if !class.Valid() {
		return apperr.Invalid("classification must be one of timing, miscount, damaged, shrinkage, found, unresolved")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	v, err := scanVariance(tx.QueryRowContext(ctx, `SELECT `+varianceCols+` FROM variances WHERE warehouse_id=$1 AND id=$2 FOR UPDATE`, string(wid), id))
	if err != nil {
		return err
	}
	if v.State == "closed" && v.Classification == class {
		return nil // repeat of the same decision
	}
	if v.State != "open" {
		return ErrBadState
	}
	if class != v.Proposed && strings.TrimSpace(note) == "" {
		return apperr.Invalid("a note is required when the classification differs from the proposal (%s)", v.Proposed)
	}
	// Adjustments need a number the rules agree with. Without one, recount first.
	if (class == Shrinkage && v.Variance <= 0) || (class == Found && v.Variance >= 0) {
		return apperr.Invalid("the counted quantity does not support %s (variance %d)", class, v.Variance)
	}
	if class == Damaged && v.DamagedQty <= 0 {
		return apperr.Invalid("no units were marked damaged")
	}

	var countTask string
	if err := tx.QueryRowContext(ctx, `
		SELECT task_id FROM counts WHERE warehouse_id=$1 AND id=COALESCE(NULLIF((SELECT resolved_count_id FROM variances WHERE warehouse_id=$1 AND id=$2),''), $3)`,
		string(wid), id, v.CountID).Scan(&countTask); err != nil {
		return err
	}

	var movementID string
	loc := inventory.LocationCode(v.Location)
	post := func(kind inventory.MovementKind, from inventory.LocationCode, fromState inventory.StockState, to inventory.LocationCode, toState inventory.StockState, qty int64) error {
		movementID = ids.New("mvt")
		err := s.store.AppendMovementTx(ctx, tx, inventory.Movement{
			ID: movementID, WarehouseID: wid, Kind: kind,
			FromLocation: from, FromState: fromState, ToLocation: to, ToState: toState,
			SKUID: inventory.SKUID(v.SKU), LotID: inventory.LotID(v.Lot), LPNID: inventory.LPNID(v.LPN), Qty: qty,
			OccurredAt: s.now(), IdempotencyKey: "variance-" + id, TaskID: countTask,
		})
		if errors.Is(err, inventory.ErrMovementConflict) {
			return fmt.Errorf("%w: the stock to adjust is reserved or already picked; wait for those to finish or recount", err)
		}
		return err
	}
	switch class {
	case Shrinkage:
		err = post(inventory.KindCountAdjust, loc, inventory.StateAvailable, inventory.VirtualVariance, inventory.StateAvailable, v.Variance)
	case Found:
		err = post(inventory.KindCountAdjust, inventory.VirtualVariance, inventory.StateAvailable, loc, inventory.StateAvailable, -v.Variance)
	case Damaged:
		err = post(inventory.KindQuarantine, loc, inventory.StateAvailable, loc, inventory.StateQuarantined, v.DamagedQty)
	}
	if err != nil {
		return err
	}

	newState, closedAt := "closed", s.now()
	if class == Unresolved {
		newState = "open"
	}
	var closed *time.Time
	if newState == "closed" {
		closed = &closedAt
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE variances SET classification=$3, state=$4, closed_at=$5, confirmed_by=$6, note=$7, adjustment_movement_id=$8,
		       flagged = flagged OR $3 = 'shrinkage'
		WHERE warehouse_id=$1 AND id=$2`,
		string(wid), id, string(class), newState, closed, actor, note, movementID); err != nil {
		return err
	}
	if err := outbox.Emit(ctx, tx, string(wid), "count.variance_classified", id, map[string]any{
		"variance_id": id, "location": v.Location, "sku": v.SKU, "lot": v.Lot, "classification": class,
		"variance": v.Variance, "adjustment_movement_id": movementID, "confirmed_by": actor,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// Ageing buckets open variances by the days since they opened (spec 8.4).
func (s *Service) Ageing(ctx context.Context, wid inventory.WarehouseID) (Ageing, error) {
	return AgeingFor(ctx, s.db, `variances`, `state <> 'closed'`, wid, s.now())
}

// AgeingFor buckets rows of table matching where by opened_at. table and where
// are code constants, never user input.
func AgeingFor(ctx context.Context, db *sql.DB, table, where string, wid inventory.WarehouseID, now time.Time) (Ageing, error) {
	var a Ageing
	err := db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE opened_at >  $2::timestamptz - interval '7 days'),
		       count(*) FILTER (WHERE opened_at <= $2::timestamptz - interval '7 days'  AND opened_at > $2::timestamptz - interval '30 days'),
		       count(*) FILTER (WHERE opened_at <= $2::timestamptz - interval '30 days' AND opened_at > $2::timestamptz - interval '90 days'),
		       count(*) FILTER (WHERE opened_at <= $2::timestamptz - interval '90 days')
		FROM `+table+` WHERE warehouse_id=$1 AND `+where, string(wid), now).Scan(&a.Under7, &a.D7to30, &a.D30to90, &a.Over90)
	return a, err
}
