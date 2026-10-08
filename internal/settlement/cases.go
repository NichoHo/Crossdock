package settlement

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/counting"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbox"
)

// Break is a settlement case: the shipment, the outcome, the remittance line
// and the delta (spec 9.2).
type Break struct {
	ID, Kind, ShipmentID, Tracking, RemittanceLineID, OutcomeID string
	Expected, Actual, Delta                                      int64
	Detail, State, Resolution, WriteoffReason, Note, ResolvedBy  string
	OpenedAt                                                      time.Time
	ClosedAt                                                      *time.Time
	// Carrier, CourierRef and Remittance describe the line, when there is one.
	Carrier, CourierRef, RemittanceID string
}

const breakCols = `b.id, b.kind, b.shipment_id, b.tracking, b.remittance_line_id, b.outcome_id, b.expected_minor, b.actual_minor, b.delta_minor,
	b.detail, b.state, b.resolution, b.writeoff_reason, b.note, b.resolved_by, b.opened_at, b.closed_at,
	COALESCE(sh.carrier,''), COALESCE(rl.courier_ref,''), COALESCE(rl.remittance_id,'')`

const breakFrom = `FROM settlement_breaks b
	LEFT JOIN shipments sh ON sh.warehouse_id=b.warehouse_id AND sh.id=b.shipment_id
	LEFT JOIN remittance_lines rl ON rl.warehouse_id=b.warehouse_id AND rl.id=b.remittance_line_id`

func scanBreak(r interface{ Scan(...any) error }) (Break, error) {
	var b Break
	err := r.Scan(&b.ID, &b.Kind, &b.ShipmentID, &b.Tracking, &b.RemittanceLineID, &b.OutcomeID, &b.Expected, &b.Actual, &b.Delta,
		&b.Detail, &b.State, &b.Resolution, &b.WriteoffReason, &b.Note, &b.ResolvedBy, &b.OpenedAt, &b.ClosedAt, &b.Carrier, &b.CourierRef, &b.RemittanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// ListBreaks pages cases oldest first. state "" is all, "unclosed" is open.
func (s *Service) ListBreaks(ctx context.Context, wid inventory.WarehouseID, state, kind string, afterOpened time.Time, afterID string, limit int) ([]Break, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+breakCols+` `+breakFrom+`
		WHERE b.warehouse_id=$1 AND ($2='' OR ($2='unclosed' AND b.state='open') OR b.state=$2) AND ($3='' OR b.kind=$3)
		  AND ($5='' OR (b.opened_at, b.id) > ($4, $5))
		ORDER BY b.opened_at, b.id LIMIT $6`, string(wid), state, kind, afterOpened, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Break
	for rows.Next() {
		b, err := scanBreak(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Service) GetBreak(ctx context.Context, wid inventory.WarehouseID, id string) (Break, error) {
	return scanBreak(s.db.QueryRowContext(ctx, `SELECT `+breakCols+` `+breakFrom+` WHERE b.warehouse_id=$1 AND b.id=$2`, string(wid), id))
}

// Resolutions of a case.
const (
	ResRecovered  = "recovered"
	ResWrittenOff = "written_off"
	ResNoError    = "no_error"
)

// WriteoffReasons classify money that is written off (spec 9.2).
var WriteoffReasons = map[string]bool{
	"courier_fee_dispute": true, "customer_short_paid": true, "lost_in_transit": true, "data_error": true, "goodwill": true,
}

// Resolve closes a case. It is a supervisor's decision and needs a note. Money
// written off also needs a classified reason, and books the write-off against the
// shipment; money recovered books the recovery. Repeating the same resolution is
// a no-op.
func (s *Service) Resolve(ctx context.Context, wid inventory.WarehouseID, id, resolution, note, reason, actor string) error {
	if resolution != ResRecovered && resolution != ResWrittenOff && resolution != ResNoError {
		return apperr.Invalid("resolution must be recovered, written_off or no_error")
	}
	if strings.TrimSpace(note) == "" {
		return apperr.Invalid("a note is required to resolve a case")
	}
	if resolution == ResWrittenOff && !WriteoffReasons[reason] {
		return apperr.Invalid("writing money off needs a reason: courier_fee_dispute, customer_short_paid, lost_in_transit, data_error or goodwill")
	}
	if resolution != ResWrittenOff {
		reason = ""
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var b Break
	b, err = scanBreak(tx.QueryRowContext(ctx, `SELECT `+breakCols+` `+breakFrom+` WHERE b.warehouse_id=$1 AND b.id=$2 FOR UPDATE OF b`, string(wid), id))
	if err != nil {
		return err
	}
	if b.State == "resolved" && b.Resolution == resolution {
		return nil
	}
	if b.State != "open" {
		return apperr.Invalid("this case is already %s", b.State)
	}
	now := s.now()
	switch resolution {
	case ResWrittenOff:
		err = book(ctx, tx, wid, b.ShipmentID, "writeoff", b.Delta, "case-"+b.ID, reason, now)
	case ResRecovered:
		err = book(ctx, tx, wid, b.ShipmentID, "recovered", b.Delta, "case-"+b.ID, note, now)
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE settlement_breaks SET state='resolved', resolution=$3, writeoff_reason=$4, note=$5, resolved_by=$6, closed_at=$7
		WHERE warehouse_id=$1 AND id=$2`, string(wid), id, resolution, reason, note, actor, now); err != nil {
		return err
	}
	if err := outbox.Emit(ctx, tx, string(wid), "settlement.break_resolved", firstNonEmpty(b.ShipmentID, id), map[string]any{
		"break_id": id, "kind": b.Kind, "resolution": resolution, "writeoff_reason": reason, "delta_minor": b.Delta, "resolved_by": actor,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Ageing buckets open cases by days since they opened (spec 8.4).
func (s *Service) Ageing(ctx context.Context, wid inventory.WarehouseID) (counting.Ageing, error) {
	return counting.AgeingFor(ctx, s.db, `settlement_breaks`, `state = 'open'`, wid, s.now())
}

// ---------------------------------------------------------------------------
// Shipments with money at stake
// ---------------------------------------------------------------------------

// ShipmentRow is a COD shipment with where it stands.
type ShipmentRow struct {
	ID, OrderID, Carrier, Tracking string
	COD                             int64
	DispatchedAt                    time.Time
	// Status: dispatched, delivered, rejected, lost, remitted or in_dispute.
	Status     string
	OpenCases  int
	Outcome    string
}

// ListShipments pages COD shipments newest first.
func (s *Service) ListShipments(ctx context.Context, wid inventory.WarehouseID, status string, before time.Time, beforeID string, limit int) ([]ShipmentRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if beforeID == "" {
		before, beforeID = time.Now().Add(100*365*24*time.Hour), "\U0010FFFF"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT * FROM (
		  SELECT sh.id, sh.order_id, sh.carrier, sh.tracking, sh.cod_amount_minor, sh.dispatched_at,
		    COALESCE((SELECT o.outcome FROM delivery_outcomes o WHERE o.warehouse_id=sh.warehouse_id AND o.shipment_id=sh.id ORDER BY o.reported_at DESC, o.id DESC LIMIT 1),'') AS outcome,
		    (SELECT count(*) FROM settlement_breaks b WHERE b.warehouse_id=sh.warehouse_id AND b.shipment_id=sh.id AND b.state='open') AS open_cases,
		    EXISTS (SELECT 1 FROM remittance_lines rl WHERE rl.warehouse_id=sh.warehouse_id AND rl.shipment_id=sh.id AND rl.state='matched') AS remitted
		  FROM shipments sh
		  WHERE sh.warehouse_id=$1 AND sh.cod_amount_minor IS NOT NULL AND (sh.dispatched_at, sh.id) < ($2, $3)
		) x
		WHERE ($4 = '' OR CASE WHEN open_cases > 0 THEN 'in_dispute' WHEN remitted THEN 'remitted' WHEN outcome = 'delivered_collected' THEN 'delivered'
		                       WHEN outcome = '' THEN 'dispatched' ELSE outcome END = $4)
		ORDER BY dispatched_at DESC, id DESC LIMIT $5`, string(wid), before, beforeID, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShipmentRow
	for rows.Next() {
		var r ShipmentRow
		var remitted bool
		var cod *int64
		if err := rows.Scan(&r.ID, &r.OrderID, &r.Carrier, &r.Tracking, &cod, &r.DispatchedAt, &r.Outcome, &r.OpenCases, &remitted); err != nil {
			return nil, err
		}
		if cod != nil {
			r.COD = *cod
		}
		switch {
		case r.OpenCases > 0:
			r.Status = "in_dispute"
		case remitted:
			r.Status = "remitted"
		case r.Outcome == DeliveredCollected:
			r.Status = "delivered"
		case r.Outcome == "":
			r.Status = "dispatched"
		default:
			r.Status = r.Outcome
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// The one timeline (spec 9.3)
// ---------------------------------------------------------------------------

// Event is one line of a shipment's story.
type Event struct {
	At   time.Time
	Kind string
	Text string
	Ref  string
}

// Timeline joins everything that happened to one shipment, physical and
// financial, in time order: dispatched, the courier's outcome, the return,
// where the units ended up, what it cost, and what the remittance said.
func (s *Service) Timeline(ctx context.Context, wid inventory.WarehouseID, shipmentID string) ([]Event, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM shipments WHERE warehouse_id=$1 AND id=$2)`, string(wid), shipmentID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT at, kind, text, ref FROM (
		  SELECT dispatched_at AS at, 'dispatched' AS kind,
		         'Dispatched with ' || carrier || ', tracking ' || tracking ||
		         COALESCE(', cash on delivery ' || (cod_amount_minor/100) || '.' || lpad((cod_amount_minor%100)::text, 2, '0'), ', prepaid') AS text, id AS ref, 1 AS ord
		    FROM shipments WHERE warehouse_id=$1 AND id=$2
		  UNION ALL
		  SELECT reported_at, 'outcome', 'Courier reported ' || replace(outcome, '_', ' '), id, 2
		    FROM delivery_outcomes WHERE warehouse_id=$1 AND shipment_id=$2
		  UNION ALL
		  SELECT created_at, 'return_authorised', 'Return authorised (' || replace(reason, '_', ' ') || ')', id, 3
		    FROM return_authorisations WHERE warehouse_id=$1 AND shipment_id=$2
		  UNION ALL
		  SELECT l.received_at, 'returned', 'Returned: ' || l.qty || ' x ' || l.sku_id || ', ' || l.condition || ', received into ' || l.location, l.id, 4
		    FROM return_lines l JOIN return_authorisations r ON r.warehouse_id=l.warehouse_id AND r.id=l.ra_id
		    WHERE r.warehouse_id=$1 AND r.shipment_id=$2
		  UNION ALL
		  SELECT l.dispositioned_at, 'dispositioned',
		         CASE l.decision WHEN 'restock' THEN 'Restocked ' WHEN 'quarantine' THEN 'Quarantined ' ELSE 'Scrapped ' END || l.qty || ' x ' || l.sku_id ||
		         CASE l.decision WHEN 'scrap' THEN '' ELSE ' to ' || l.destination END, l.id, 5
		    FROM return_lines l JOIN return_authorisations r ON r.warehouse_id=l.warehouse_id AND r.id=l.ra_id
		    WHERE r.warehouse_id=$1 AND r.shipment_id=$2 AND l.dispositioned_at IS NOT NULL
		  UNION ALL
		  SELECT created_at, 'ledger',
		         CASE kind WHEN 'cod_receivable' THEN 'Courier owes ' WHEN 'shipping_cost_lost' THEN 'Shipping cost lost: ' WHEN 'claim_owed' THEN 'Claim owed by courier: '
		                   WHEN 'remitted' THEN 'Cash received: ' WHEN 'fee_deducted' THEN 'Courier fee kept: ' WHEN 'writeoff' THEN 'Written off: ' ELSE 'Recovered: ' END ||
		         (abs(amount_minor)/100) || '.' || lpad((abs(amount_minor)%100)::text, 2, '0'), id, 6
		    FROM ledger WHERE warehouse_id=$1 AND shipment_id=$2
		  UNION ALL
		  SELECT rm.received_at, 'remittance', CASE rl.state WHEN 'matched' THEN 'Remittance line matched' ELSE 'Remittance line did not match' END ||
		         ' (' || rm.filename || ', net ' || (rl.net_minor/100) || '.' || lpad((rl.net_minor%100)::text, 2, '0') || ')', rl.id, 7
		    FROM remittance_lines rl JOIN remittances rm ON rm.warehouse_id=rl.warehouse_id AND rm.id=rl.remittance_id
		    WHERE rl.warehouse_id=$1 AND rl.shipment_id=$2
		  UNION ALL
		  SELECT opened_at, 'break_opened', 'Case opened: ' || replace(kind, '_', ' ') || ', short by ' || (delta_minor/100) || '.' || lpad((abs(delta_minor)%100)::text, 2, '0'), id, 8
		    FROM settlement_breaks WHERE warehouse_id=$1 AND shipment_id=$2
		  UNION ALL
		  SELECT closed_at, 'break_closed', 'Case ' || state || CASE WHEN resolution <> '' THEN ' (' || replace(resolution, '_', ' ') || ')' ELSE '' END, id, 9
		    FROM settlement_breaks WHERE warehouse_id=$1 AND shipment_id=$2 AND closed_at IS NOT NULL
		) t ORDER BY at, ord, ref`, string(wid), shipmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.At, &e.Kind, &e.Text, &e.Ref); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
