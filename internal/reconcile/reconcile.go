// Package reconcile is three-way inbound reconciliation (spec 8.3): for every
// ASN line, what the supplier said, what was scanned, and what reached a shelf.
package reconcile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/counting"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
)

var ErrNotFound = errors.New("reconcile: break not found")

const (
	ShortShipped      = "short_shipped"
	OverShipped       = "over_shipped"
	PutawayIncomplete = "putaway_incomplete"
	LotMismatch       = "lot_mismatch"
)

// Service finds and tracks inbound breaks.
type Service struct {
	db *sql.DB
	// Grace is how long a receipt may wait for its putaway before that is a break.
	Grace time.Duration
	now   func() time.Time
}

func NewService(db *sql.DB) *Service {
	return &Service{db: db, Grace: 2 * time.Hour, now: time.Now}
}

func (s *Service) SetClock(now func() time.Time) { s.now = now }

type found struct {
	kind, detail string
}

type lineFacts struct {
	asnID, asnRef, supplier, lineID, sku, expectedLot string
	expected, received, putaway                       int64
	short                                             bool
	badLots                                           int
	oldestReceipt                                     *time.Time
	putawayException                                  bool
}

// Run compares the three records for every ASN line and brings the break list
// up to date: new breaks open, breaks whose condition went away clear
// themselves (a late putaway), and a supervisor's waiver stays. It is idempotent,
// so it can run on a timer. It returns how many breaks it opened and cleared.
func (s *Service) Run(ctx context.Context, wid inventory.WarehouseID) (opened, cleared int, err error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.ref, a.supplier, l.id, l.sku_id, l.lot_id, l.expected_qty, l.received_qty, l.short,
		       COALESCE((SELECT SUM(t.qty) FROM tasks t JOIN receipts r ON r.warehouse_id=t.warehouse_id AND r.id=t.receipt_id
		                 WHERE t.warehouse_id=l.warehouse_id AND r.asn_line_id=l.id AND t.kind='putaway' AND t.state='completed'), 0),
		       (SELECT count(*) FROM receipts r WHERE r.warehouse_id=l.warehouse_id AND r.asn_line_id=l.id AND l.lot_id <> '' AND r.lot_id <> l.lot_id),
		       (SELECT min(r.created_at) FROM receipts r WHERE r.warehouse_id=l.warehouse_id AND r.asn_line_id=l.id),
		       EXISTS (SELECT 1 FROM tasks t JOIN receipts r ON r.warehouse_id=t.warehouse_id AND r.id=t.receipt_id
		               WHERE t.warehouse_id=l.warehouse_id AND r.asn_line_id=l.id AND t.kind='putaway' AND t.state='exception')
		FROM asn_lines l JOIN asns a ON a.warehouse_id=l.warehouse_id AND a.id=l.asn_id
		WHERE l.warehouse_id=$1`, string(wid))
	if err != nil {
		return 0, 0, err
	}
	var facts []lineFacts
	for rows.Next() {
		var f lineFacts
		if err := rows.Scan(&f.asnID, &f.asnRef, &f.supplier, &f.lineID, &f.sku, &f.expectedLot, &f.expected, &f.received, &f.short,
			&f.putaway, &f.badLots, &f.oldestReceipt, &f.putawayException); err != nil {
			rows.Close()
			return 0, 0, err
		}
		facts = append(facts, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	// Current state of the break list, to diff against.
	existing := map[string]string{} // lineID|kind -> state
	erows, err := tx.QueryContext(ctx, `SELECT asn_line_id, kind, state FROM recon_breaks WHERE warehouse_id=$1`, string(wid))
	if err != nil {
		return 0, 0, err
	}
	for erows.Next() {
		var line, kind, state string
		if err := erows.Scan(&line, &kind, &state); err != nil {
			erows.Close()
			return 0, 0, err
		}
		existing[line+"|"+kind] = state
	}
	erows.Close()

	holds := map[string]bool{}
	for _, f := range facts {
		for _, b := range evaluate(f, now, s.Grace) {
			holds[f.lineID+"|"+b.kind] = true
			prev, had := existing[f.lineID+"|"+b.kind]
			switch {
			case !had:
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO recon_breaks (id, warehouse_id, kind, asn_id, asn_ref, supplier, asn_line_id, sku_id, expected_qty, received_qty, putaway_qty, detail, opened_at)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
					ids.New("brk"), string(wid), b.kind, f.asnID, f.asnRef, f.supplier, f.lineID, f.sku, f.expected, f.received, f.putaway, b.detail, now); err != nil {
					return 0, 0, err
				}
				opened++
			case prev == "waived":
				// A supervisor accepted it. Leave it.
			default:
				// Refresh the numbers; reopen if it had cleared and came back.
				reopen := prev == "cleared"
				if _, err := tx.ExecContext(ctx, `
					UPDATE recon_breaks SET expected_qty=$4, received_qty=$5, putaway_qty=$6, detail=$7,
					       state='open', closed_at=NULL, opened_at = CASE WHEN $8 THEN $9::timestamptz ELSE opened_at END
					WHERE warehouse_id=$1 AND asn_line_id=$2 AND kind=$3`,
					string(wid), f.lineID, b.kind, f.expected, f.received, f.putaway, b.detail, reopen, now); err != nil {
					return 0, 0, err
				}
				if reopen {
					opened++
				}
			}
		}
	}
	for k, st := range existing {
		if st != "open" || holds[k] {
			continue
		}
		line, kind, _ := strings.Cut(k, "|")
		if _, err := tx.ExecContext(ctx, `UPDATE recon_breaks SET state='cleared', closed_at=$4 WHERE warehouse_id=$1 AND asn_line_id=$2 AND kind=$3`,
			string(wid), line, kind, now); err != nil {
			return 0, 0, err
		}
		cleared++
	}
	return opened, cleared, tx.Commit()
}

// evaluate is the rule set. It is pure, so each break kind is a table row in a test.
func evaluate(f lineFacts, now time.Time, grace time.Duration) []found {
	var out []found
	if f.short && f.received < f.expected {
		out = append(out, found{ShortShipped, fmt.Sprintf("supplier said %d, %d arrived", f.expected, f.received)})
	}
	if f.received > f.expected {
		out = append(out, found{OverShipped, fmt.Sprintf("supplier said %d, %d arrived", f.expected, f.received)})
	}
	if f.received > f.putaway {
		old := f.oldestReceipt != nil && now.Sub(*f.oldestReceipt) >= grace
		if old || f.putawayException {
			why := fmt.Sprintf("%d received, %d put away", f.received, f.putaway)
			if f.putawayException {
				why += ", a putaway is in exception"
			}
			out = append(out, found{PutawayIncomplete, why})
		}
	}
	if f.badLots > 0 {
		out = append(out, found{LotMismatch, fmt.Sprintf("%d receipt(s) carry a lot other than %s", f.badLots, f.expectedLot)})
	}
	return out
}

// Break is one reconciliation break for the workbench.
type Break struct {
	ID, Kind, ASNID, ASNRef, Supplier, ASNLineID, SKU string
	Expected, Received, Putaway                       int64
	Detail, State, Note, ClosedBy                     string
	OpenedAt                                          time.Time
	ClosedAt                                          *time.Time
}

// List pages breaks oldest first. state "" is all, "unclosed" is open.
func (s *Service) List(ctx context.Context, wid inventory.WarehouseID, state, kind string, afterOpened time.Time, afterID string, limit int) ([]Break, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, asn_id, asn_ref, supplier, asn_line_id, sku_id, expected_qty, received_qty, putaway_qty, detail, state, note, closed_by, opened_at, closed_at
		FROM recon_breaks
		WHERE warehouse_id=$1 AND ($2='' OR ($2='unclosed' AND state='open') OR state=$2) AND ($3='' OR kind=$3)
		  AND ($5='' OR (opened_at, id) > ($4, $5))
		ORDER BY opened_at, id LIMIT $6`, string(wid), state, kind, afterOpened, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Break
	for rows.Next() {
		var b Break
		if err := rows.Scan(&b.ID, &b.Kind, &b.ASNID, &b.ASNRef, &b.Supplier, &b.ASNLineID, &b.SKU, &b.Expected, &b.Received, &b.Putaway,
			&b.Detail, &b.State, &b.Note, &b.ClosedBy, &b.OpenedAt, &b.ClosedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Waive accepts a break with a note ("the supplier agreed a credit note").
func (s *Service) Waive(ctx context.Context, wid inventory.WarehouseID, id, note, actor string) error {
	if strings.TrimSpace(note) == "" {
		return apperr.Invalid("a note is required to waive a break")
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE recon_breaks SET state='waived', note=$3, closed_by=$4, closed_at=$5
		WHERE warehouse_id=$1 AND id=$2 AND state='open'`, string(wid), id, note, actor, s.now())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists bool
		_ = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recon_breaks WHERE warehouse_id=$1 AND id=$2)`, string(wid), id).Scan(&exists)
		if !exists {
			return ErrNotFound
		}
		return nil // already closed: repeating is a no-op
	}
	return nil
}

// Ageing buckets open breaks by days since they opened (spec 8.4).
func (s *Service) Ageing(ctx context.Context, wid inventory.WarehouseID) (counting.Ageing, error) {
	return counting.AgeingFor(ctx, s.db, `recon_breaks`, `state = 'open'`, wid, s.now())
}
