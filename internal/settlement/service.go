package settlement

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbox"
	"github.com/nicholasgasior/crossdock/internal/returns"
)

var ErrNotFound = errors.New("settlement: not found")

type Service struct {
	db  *sql.DB
	now func() time.Time
}

func NewService(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }

func (s *Service) SetClock(now func() time.Time) { s.now = now }

type querier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// ---------------------------------------------------------------------------
// Carrier terms
// ---------------------------------------------------------------------------

// CarrierTerms is a courier's terms as stored.
type CarrierTerms struct {
	Carrier string
	Terms
}

func termsFor(ctx context.Context, q querier, wid inventory.WarehouseID, carrier string) (Terms, error) {
	t := DefaultTerms
	err := q.QueryRowContext(ctx, `
		SELECT cod_fee_bps, cod_fee_flat_minor, shipping_cost_minor, settlement_days FROM carrier_terms WHERE warehouse_id=$1 AND carrier=$2`,
		string(wid), carrier).Scan(&t.FeeBps, &t.FeeFlat, &t.ShippingCost, &t.SettlementDays)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultTerms, nil
	}
	return t, err
}

func (s *Service) PutTerms(ctx context.Context, wid inventory.WarehouseID, carrier string, t Terms) error {
	if carrier == "" || t.FeeBps < 0 || t.FeeBps > 10000 || t.FeeFlat < 0 || t.ShippingCost < 0 || t.SettlementDays < 0 || t.SettlementDays > 365 {
		return apperr.Invalid("carrier is required; fee_bps 0 to 10000; amounts and days must not be negative")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO carrier_terms (warehouse_id, carrier, cod_fee_bps, cod_fee_flat_minor, shipping_cost_minor, settlement_days)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (warehouse_id, carrier) DO UPDATE SET cod_fee_bps=$3, cod_fee_flat_minor=$4, shipping_cost_minor=$5, settlement_days=$6`,
		string(wid), carrier, t.FeeBps, t.FeeFlat, t.ShippingCost, t.SettlementDays)
	return err
}

func (s *Service) ListTerms(ctx context.Context, wid inventory.WarehouseID) ([]CarrierTerms, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT carrier, cod_fee_bps, cod_fee_flat_minor, shipping_cost_minor, settlement_days FROM carrier_terms WHERE warehouse_id=$1 ORDER BY carrier`, string(wid))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CarrierTerms
	for rows.Next() {
		var c CarrierTerms
		if err := rows.Scan(&c.Carrier, &c.FeeBps, &c.FeeFlat, &c.ShippingCost, &c.SettlementDays); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Ledger
// ---------------------------------------------------------------------------

// book writes a ledger entry. (shipment, kind, ref) is unique, so booking the
// same thing twice does nothing the second time.
func book(ctx context.Context, q querier, wid inventory.WarehouseID, shipmentID, kind string, amount int64, ref, note string, at time.Time) error {
	if amount == 0 || shipmentID == "" {
		return nil
	}
	_, err := q.ExecContext(ctx, `
		INSERT INTO ledger (id, warehouse_id, shipment_id, kind, amount_minor, ref, note, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (warehouse_id, shipment_id, kind, ref) DO NOTHING`,
		ids.New("led"), string(wid), shipmentID, kind, amount, ref, note, at)
	return err
}

// Summary totals the ledger. Receivable is what couriers still owe: COD
// delivered, less what they paid, the fees they kept, what was written off and
// what was recovered later.
type Summary struct {
	CODDelivered     int64
	Remitted         int64
	FeesDeducted     int64
	WrittenOff       int64
	Recovered        int64
	ShippingCostLost int64 // positive number: money spent on parcels that came back
	ClaimsOwed       int64
	Receivable       int64
}

func (s *Service) Summary(ctx context.Context, wid inventory.WarehouseID) (Summary, error) {
	var sm Summary
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(amount_minor) FILTER (WHERE kind='cod_receivable'),0),
		       COALESCE(SUM(amount_minor) FILTER (WHERE kind='remitted'),0),
		       COALESCE(SUM(amount_minor) FILTER (WHERE kind='fee_deducted'),0),
		       COALESCE(SUM(amount_minor) FILTER (WHERE kind='writeoff'),0),
		       COALESCE(SUM(amount_minor) FILTER (WHERE kind='recovered'),0),
		       COALESCE(-SUM(amount_minor) FILTER (WHERE kind='shipping_cost_lost'),0),
		       COALESCE(SUM(amount_minor) FILTER (WHERE kind='claim_owed'),0)
		FROM ledger WHERE warehouse_id=$1`, string(wid)).
		Scan(&sm.CODDelivered, &sm.Remitted, &sm.FeesDeducted, &sm.WrittenOff, &sm.Recovered, &sm.ShippingCostLost, &sm.ClaimsOwed)
	sm.Receivable = sm.CODDelivered - sm.Remitted - sm.FeesDeducted - sm.WrittenOff - sm.Recovered
	return sm, err
}

// ---------------------------------------------------------------------------
// Delivery outcomes (spec 9.1)
// ---------------------------------------------------------------------------

// ShipmentRef finds a shipment by id, or by tracking number (what couriers use).
type ShipmentRef struct{ ID, Tracking, Carrier string }

// OutcomeResult says what recording an outcome did.
type OutcomeResult struct {
	OutcomeID             string
	ShipmentID            string
	ReturnAuthorisationID string
	Duplicate             bool
}

// RecordOutcome stores a courier's report on a shipment and books its money
// effect: a delivered COD parcel makes the courier owe the amount, a rejected
// parcel loses its shipping cost and opens a return authorisation, a lost
// parcel opens a claim. Repeating the same outcome changes nothing.
func (s *Service) RecordOutcome(ctx context.Context, wid inventory.WarehouseID, ref ShipmentRef, outcome string, reportedAt time.Time, source string) (OutcomeResult, error) {
	if outcome != DeliveredCollected && outcome != Rejected && outcome != Lost {
		return OutcomeResult{}, apperr.Invalid("outcome must be delivered_collected, rejected or lost")
	}
	if ref.ID == "" && ref.Tracking == "" {
		return OutcomeResult{}, apperr.Invalid("give a shipment id or a tracking number")
	}
	if reportedAt.IsZero() {
		reportedAt = s.now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OutcomeResult{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	var shipmentID, orderID, carrier string
	var codAmt *int64
	err = tx.QueryRowContext(ctx, `
		SELECT id, order_id, carrier, cod_amount_minor FROM shipments
		WHERE warehouse_id=$1 AND (($2 <> '' AND id=$2) OR ($2 = '' AND tracking=$3 AND ($4 = '' OR carrier=$4)))
		ORDER BY dispatched_at DESC LIMIT 1`, string(wid), ref.ID, ref.Tracking, ref.Carrier).Scan(&shipmentID, &orderID, &carrier, &codAmt)
	if errors.Is(err, sql.ErrNoRows) {
		return OutcomeResult{}, ErrNotFound
	}
	if err != nil {
		return OutcomeResult{}, err
	}
	res := OutcomeResult{ShipmentID: shipmentID, OutcomeID: ids.New("out")}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO delivery_outcomes (id, warehouse_id, shipment_id, outcome, reported_at, source) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (warehouse_id, shipment_id, outcome) DO NOTHING RETURNING id`,
		res.OutcomeID, string(wid), shipmentID, outcome, reportedAt, source).Scan(&res.OutcomeID)
	if errors.Is(err, sql.ErrNoRows) {
		res.Duplicate = true
		_ = tx.QueryRowContext(ctx, `SELECT id FROM delivery_outcomes WHERE warehouse_id=$1 AND shipment_id=$2 AND outcome=$3`, string(wid), shipmentID, outcome).Scan(&res.OutcomeID)
		return res, nil
	}
	if err != nil {
		return res, err
	}
	terms, err := termsFor(ctx, tx, wid, carrier)
	if err != nil {
		return res, err
	}
	at := s.now() // when we booked it; reportedAt is when the courier says it happened
	switch outcome {
	case DeliveredCollected:
		if codAmt != nil {
			err = book(ctx, tx, wid, shipmentID, "cod_receivable", *codAmt, res.OutcomeID, "courier collected cash on delivery", at)
		}
	case Rejected:
		if err = book(ctx, tx, wid, shipmentID, "shipping_cost_lost", -terms.ShippingCost, res.OutcomeID, "parcel came back rejected", at); err != nil {
			return res, err
		}
		res.ReturnAuthorisationID, _, err = returns.CreateRA(ctx, tx, wid, shipmentID, orderID, "cod_rejected", at)
	case Lost:
		claim := terms.ShippingCost
		if codAmt != nil {
			claim = *codAmt
		}
		err = book(ctx, tx, wid, shipmentID, "claim_owed", claim, res.OutcomeID, "courier lost the parcel", at)
	}
	if err != nil {
		return res, err
	}
	if err := outbox.Emit(ctx, tx, string(wid), "shipment.delivery_outcome", shipmentID, map[string]any{
		"shipment_id": shipmentID, "order_id": orderID, "outcome": outcome, "reported_at": reportedAt, "return_authorisation_id": res.ReturnAuthorisationID,
	}); err != nil {
		return res, err
	}
	return res, tx.Commit()
}
