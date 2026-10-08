package settlement

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbox"
)

// RemitLine is one parsed line of a courier remittance file (spec 9.4).
type RemitLine struct {
	No                  int
	CourierRef          string
	Tracking            string
	Collected, Fee, Net int64
}

// ParseRemittance reads a remittance CSV. Required columns, in any order:
// courier_ref, tracking, collected, fee, net. Amounts are decimals with at most
// two places. A bad file is refused whole, with every bad row named, so a
// half-read file never reaches the books.
func ParseRemittance(data []byte) ([]RemitLine, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	head, err := r.Read()
	if err != nil {
		return nil, apperr.Invalid("the file is empty or is not a CSV")
	}
	col := map[string]int{}
	for i, h := range head {
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))] = i
	}
	for _, need := range []string{"courier_ref", "tracking", "collected", "fee", "net"} {
		if _, ok := col[need]; !ok {
			return nil, apperr.Invalid("the header row has no %q column (needs courier_ref, tracking, collected, fee, net)", need)
		}
	}
	var out []RemitLine
	var problems []string
	for no := 1; ; no++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("row %d: %v", no, err))
			continue
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			no--
			continue
		}
		get := func(name string) string {
			if i := col[name]; i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		l := RemitLine{No: no, CourierRef: get("courier_ref"), Tracking: get("tracking")}
		var bad []string
		if l.Tracking == "" {
			bad = append(bad, "tracking is empty")
		}
		for name, dst := range map[string]*int64{"collected": &l.Collected, "fee": &l.Fee, "net": &l.Net} {
			v, perr := ParseMoney(get(name))
			if perr != nil {
				bad = append(bad, name+": "+perr.Error())
				continue
			}
			*dst = v
		}
		if len(bad) == 0 && l.Net+l.Fee > l.Collected {
			bad = append(bad, "net plus fee is more than collected")
		}
		if len(bad) > 0 {
			problems = append(problems, fmt.Sprintf("row %d: %s", no, strings.Join(bad, "; ")))
			continue
		}
		out = append(out, l)
	}
	if len(problems) > 0 {
		if len(problems) > 10 {
			problems = append(problems[:10], fmt.Sprintf("and %d more", len(problems)-10))
		}
		return nil, apperr.Invalid("the file has bad rows: %s", strings.Join(problems, " | "))
	}
	if len(out) == 0 {
		return nil, apperr.Invalid("the file has a header but no lines")
	}
	return out, nil
}

// IngestResult summarises an uploaded remittance.
type IngestResult struct {
	RemittanceID string
	Lines        int
	Matched      int
	Breaks       map[string]int
	// Duplicate is true when this exact file was uploaded before; nothing changed.
	Duplicate bool
}

// Ingest stores a remittance file and matches every line against the shipments
// and outcomes on record (spec 9.2). Matched lines book the cash received and
// the fee kept; every other line opens a settlement case. Uploading the same
// file again is a no-op.
func (s *Service) Ingest(ctx context.Context, wid inventory.WarehouseID, carrier, filename string, data []byte, actor string) (IngestResult, error) {
	if carrier == "" {
		return IngestResult{}, apperr.Invalid("carrier is required")
	}
	lines, err := ParseRemittance(data)
	if err != nil {
		return IngestResult{}, err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IngestResult{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	res := IngestResult{RemittanceID: ids.New("rem"), Lines: len(lines), Breaks: map[string]int{}}
	now := s.now()
	err = tx.QueryRowContext(ctx, `
		INSERT INTO remittances (id, warehouse_id, carrier, filename, sha256, received_by, received_at, line_count)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (warehouse_id, sha256) DO NOTHING RETURNING id`,
		res.RemittanceID, string(wid), carrier, filename, hash, actor, now, len(lines)).Scan(&res.RemittanceID)
	if errors.Is(err, sql.ErrNoRows) {
		res.Duplicate = true
		err = tx.QueryRowContext(ctx, `SELECT id, line_count, matched_count, break_count FROM remittances WHERE warehouse_id=$1 AND sha256=$2`, string(wid), hash).
			Scan(&res.RemittanceID, &res.Lines, &res.Matched, new(int))
		return res, err
	}
	if err != nil {
		return res, err
	}
	terms, err := termsFor(ctx, tx, wid, carrier)
	if err != nil {
		return res, err
	}

	for _, l := range lines {
		lineID := ids.New("rln")
		facts := LineFacts{Tracking: l.Tracking, Collected: l.Collected, Fee: l.Fee, Net: l.Net, Terms: terms}
		var shipmentID string
		var cod *int64
		err := tx.QueryRowContext(ctx, `
			SELECT id, cod_amount_minor FROM shipments WHERE warehouse_id=$1 AND carrier=$2 AND tracking=$3
			ORDER BY dispatched_at DESC LIMIT 1`, string(wid), carrier, l.Tracking).Scan(&shipmentID, &cod)
		if err == nil {
			facts.ShipmentFound, facts.COD = true, cod
			if err := tx.QueryRowContext(ctx, `
				SELECT EXISTS(SELECT 1 FROM delivery_outcomes WHERE warehouse_id=$1 AND shipment_id=$2 AND outcome='rejected'),
				       EXISTS(SELECT 1 FROM remittance_lines WHERE warehouse_id=$1 AND shipment_id=$2)`,
				string(wid), shipmentID).Scan(&facts.HasRejected, &facts.AlreadyRemitted); err != nil {
				return res, err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return res, err
		}

		verdict, broke := ClassifyLine(facts)
		state := "matched"
		if broke {
			state = "break"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO remittance_lines (id, warehouse_id, remittance_id, line_no, courier_ref, tracking, collected_minor, fee_minor, net_minor, shipment_id, state)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			lineID, string(wid), res.RemittanceID, l.No, l.CourierRef, l.Tracking, l.Collected, l.Fee, l.Net, shipmentID, state); err != nil {
			return res, err
		}
		if broke {
			res.Breaks[verdict.Kind]++
			if err := s.openBreak(ctx, tx, wid, verdict, shipmentID, l.Tracking, lineID, "", now); err != nil {
				return res, err
			}
			continue
		}
		res.Matched++
		if err := book(ctx, tx, wid, shipmentID, "remitted", l.Net, lineID, "remittance "+filename, now); err != nil {
			return res, err
		}
		if err := book(ctx, tx, wid, shipmentID, "fee_deducted", l.Fee, lineID, "courier fee", now); err != nil {
			return res, err
		}
		// A late payment clears the "unremitted" case it answers.
		if _, err := tx.ExecContext(ctx, `
			UPDATE settlement_breaks SET state='cleared', closed_at=$3, note='paid by remittance '||$4
			WHERE warehouse_id=$1 AND shipment_id=$2 AND kind='unremitted' AND state='open'`, string(wid), shipmentID, now, res.RemittanceID); err != nil {
			return res, err
		}
	}
	nb := 0
	for _, n := range res.Breaks {
		nb += n
	}
	if _, err := tx.ExecContext(ctx, `UPDATE remittances SET matched_count=$3, break_count=$4 WHERE warehouse_id=$1 AND id=$2`,
		string(wid), res.RemittanceID, res.Matched, nb); err != nil {
		return res, err
	}
	return res, tx.Commit()
}

// openBreak records a settlement case and announces it.
func (s *Service) openBreak(ctx context.Context, q querier, wid inventory.WarehouseID, v Verdict, shipmentID, tracking, lineID, outcomeID string, at time.Time) error {
	id := ids.New("stb")
	var got string
	err := q.QueryRowContext(ctx, `
		INSERT INTO settlement_breaks (id, warehouse_id, kind, shipment_id, tracking, remittance_line_id, outcome_id, expected_minor, actual_minor, delta_minor, detail, opened_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (warehouse_id, kind, shipment_id, remittance_line_id) DO NOTHING RETURNING id`,
		id, string(wid), v.Kind, shipmentID, tracking, lineID, outcomeID, v.Expected, v.Actual, v.Expected-v.Actual, v.Detail, at).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // already open
	}
	if err != nil {
		return err
	}
	return outbox.Emit(ctx, q, string(wid), "settlement.break_opened", shipmentID, map[string]any{
		"break_id": id, "kind": v.Kind, "shipment_id": shipmentID, "tracking": tracking,
		"expected_minor": v.Expected, "actual_minor": v.Actual, "delta_minor": v.Expected - v.Actual,
	})
}

// FindUnremitted opens an "unremitted" case for every delivered COD shipment the
// courier has not paid for within its settlement window (spec 9.2). Safe to run
// on a timer. It returns how many cases it opened.
func (s *Service) FindUnremitted(ctx context.Context, wid inventory.WarehouseID) (int, error) {
	now := s.now()
	rows, err := s.db.QueryContext(ctx, `
		SELECT sh.id, sh.tracking, sh.carrier, sh.cod_amount_minor, o.id, o.reported_at
		FROM shipments sh
		JOIN delivery_outcomes o ON o.warehouse_id=sh.warehouse_id AND o.shipment_id=sh.id AND o.outcome='delivered_collected'
		WHERE sh.warehouse_id=$1 AND sh.cod_amount_minor > 0
		  AND NOT EXISTS (SELECT 1 FROM remittance_lines rl WHERE rl.warehouse_id=sh.warehouse_id AND rl.shipment_id=sh.id)
		  AND NOT EXISTS (SELECT 1 FROM settlement_breaks b WHERE b.warehouse_id=sh.warehouse_id AND b.shipment_id=sh.id AND b.kind='unremitted')`,
		string(wid))
	if err != nil {
		return 0, err
	}
	type cand struct {
		shipment, tracking, carrier, outcome string
		cod                                  int64
		reported                             time.Time
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.shipment, &c.tracking, &c.carrier, &c.cod, &c.outcome, &c.reported); err != nil {
			rows.Close()
			return 0, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	opened := 0
	for _, c := range cands {
		terms, err := termsFor(ctx, s.db, wid, c.carrier)
		if err != nil {
			return opened, err
		}
		if now.Before(c.reported.Add(time.Duration(terms.SettlementDays) * 24 * time.Hour)) {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return opened, err
		}
		want := terms.ExpectedNet(c.cod)
		err = s.openBreak(ctx, tx, wid, Verdict{Unremitted, want, 0,
			fmt.Sprintf("delivered %s, no remittance within %d days", c.reported.Format("2006-01-02"), terms.SettlementDays)},
			c.shipment, c.tracking, "", c.outcome, now)
		if err != nil {
			tx.Rollback() //nolint:errcheck
			return opened, err
		}
		if err := tx.Commit(); err != nil {
			return opened, err
		}
		opened++
	}
	return opened, nil
}

// Remittance is a stored file with its match counts.
type Remittance struct {
	ID, Carrier, Filename, ReceivedBy string
	ReceivedAt                        time.Time
	Lines, Matched, Breaks            int
}

func (s *Service) ListRemittances(ctx context.Context, wid inventory.WarehouseID, limit int) ([]Remittance, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, carrier, filename, received_by, received_at, line_count, matched_count, break_count
		FROM remittances WHERE warehouse_id=$1 ORDER BY received_at DESC, id DESC LIMIT $2`, string(wid), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Remittance
	for rows.Next() {
		var r Remittance
		if err := rows.Scan(&r.ID, &r.Carrier, &r.Filename, &r.ReceivedBy, &r.ReceivedAt, &r.Lines, &r.Matched, &r.Breaks); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
