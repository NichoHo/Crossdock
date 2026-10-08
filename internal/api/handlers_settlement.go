package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/returns"
	"github.com/nicholasgasior/crossdock/internal/settlement"
)

// Returns and cash-on-delivery settlement (spec 9).

func init() {
	extraErrors = append(extraErrors, func(err error) *httpErr {
		switch {
		case errors.Is(err, settlement.ErrNotFound), errors.Is(err, returns.ErrNotFound):
			return &httpErr{http.StatusNotFound, "not found", "the requested resource does not exist"}
		case errors.Is(err, returns.ErrNothingAuthorised):
			return &httpErr{http.StatusNotFound, "no return expected", "no return is expected for that tracking number"}
		case errors.Is(err, returns.ErrNoLocation):
			return &httpErr{http.StatusConflict, "no location", err.Error()}
		case errors.Is(err, returns.ErrWrongTask):
			return &httpErr{http.StatusConflict, "wrong task", "that task is not a disposition task"}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Courier outcomes
// ---------------------------------------------------------------------------

func (s *Server) recordOutcome(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	var in struct {
		ShipmentID string     `json:"shipment_id"`
		Tracking   string     `json:"tracking"`
		Carrier    string     `json:"carrier"`
		Outcome    string     `json:"outcome"`
		ReportedAt *time.Time `json:"reported_at"`
	}
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	at := time.Time{}
	if in.ReportedAt != nil {
		at = *in.ReportedAt
	}
	res, err := s.settlement.RecordOutcome(r.Context(), wid, settlement.ShipmentRef{ID: in.ShipmentID, Tracking: in.Tracking, Carrier: in.Carrier},
		in.Outcome, at, p.ID())
	if err != nil {
		s.problem(w, r, err)
		return
	}
	status := http.StatusCreated
	if res.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"outcome_id": res.OutcomeID, "shipment_id": res.ShipmentID,
		"return_authorisation_id": res.ReturnAuthorisationID, "duplicate": res.Duplicate})
}

type shipmentRowJSON struct {
	ID           string    `json:"id"`
	OrderID      string    `json:"order_id"`
	Carrier      string    `json:"carrier"`
	Tracking     string    `json:"tracking"`
	CODMinor     int64     `json:"cod_amount_minor"`
	DispatchedAt time.Time `json:"dispatched_at"`
	Status       string    `json:"status"`
	Outcome      string    `json:"outcome,omitempty"`
	OpenCases    int       `json:"open_cases,omitempty"`
}

func (s *Server) listShipments(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	cur, err := decodeCursor(r.URL.Query().Get("cursor"), 2)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	var before time.Time
	if cur[0] != "" {
		before, _ = time.Parse(time.RFC3339Nano, cur[0])
	}
	limit := limitParam(r)
	rows, err := s.settlement.ListShipments(r.Context(), wid, r.URL.Query().Get("status"), before, cur[1], limit+1)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	out := page[shipmentRowJSON]{Items: []shipmentRowJSON{}}
	for i, x := range rows {
		if i == limit {
			out.NextCursor = encodeCursor(rows[limit-1].DispatchedAt.Format(time.RFC3339Nano), rows[limit-1].ID)
			break
		}
		out.Items = append(out.Items, shipmentRowJSON{x.ID, x.OrderID, x.Carrier, x.Tracking, x.COD, x.DispatchedAt, x.Status, x.Outcome, x.OpenCases})
	}
	writeJSON(w, 200, out)
}

func (s *Server) shipmentTimeline(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	evs, err := s.settlement.Timeline(r.Context(), wid, r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	type ev struct {
		At   time.Time `json:"at"`
		Kind string    `json:"kind"`
		Text string    `json:"text"`
		Ref  string    `json:"ref,omitempty"`
	}
	items := []ev{}
	for _, e := range evs {
		items = append(items, ev{e.At, e.Kind, e.Text, e.Ref})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// ---------------------------------------------------------------------------
// Carrier terms
// ---------------------------------------------------------------------------

type termsJSON struct {
	Carrier           string `json:"carrier,omitempty"`
	CODFeeBps         int    `json:"cod_fee_bps"`
	CODFeeFlatMinor   int64  `json:"cod_fee_flat_minor"`
	ShippingCostMinor int64  `json:"shipping_cost_minor"`
	SettlementDays    int    `json:"settlement_days"`
}

func (s *Server) listCarriers(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	ts, err := s.settlement.ListTerms(r.Context(), wid)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	items := []termsJSON{}
	for _, t := range ts {
		items = append(items, termsJSON{t.Carrier, t.FeeBps, t.FeeFlat, t.ShippingCost, t.SettlementDays})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) putCarrierTerms(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	var in termsJSON
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	err := s.settlement.PutTerms(r.Context(), wid, r.PathValue("carrier"),
		settlement.Terms{FeeBps: in.CODFeeBps, FeeFlat: in.CODFeeFlatMinor, ShippingCost: in.ShippingCostMinor, SettlementDays: in.SettlementDays})
	if err != nil {
		s.problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Remittance files, matching and cases
// ---------------------------------------------------------------------------

// uploadRemittance takes the CSV as the request body: POST ...?carrier=JNT&filename=x.csv
func (s *Server) uploadRemittance(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		s.problem(w, r, &httpErr{http.StatusRequestEntityTooLarge, "file too large", "remittance files are limited to 8 MiB"})
		return
	}
	who := p.ID()
	res, err := s.settlement.Ingest(r.Context(), wid, r.URL.Query().Get("carrier"), r.URL.Query().Get("filename"), data, who)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	status := http.StatusCreated
	if res.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"remittance_id": res.RemittanceID, "lines": res.Lines, "matched": res.Matched,
		"breaks": res.Breaks, "duplicate": res.Duplicate})
}

func (s *Server) listRemittances(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	rs, err := s.settlement.ListRemittances(r.Context(), wid, limitParam(r))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	type row struct {
		ID         string    `json:"id"`
		Carrier    string    `json:"carrier"`
		Filename   string    `json:"filename"`
		ReceivedBy string    `json:"received_by"`
		ReceivedAt time.Time `json:"received_at"`
		Lines      int       `json:"lines"`
		Matched    int       `json:"matched"`
		Breaks     int       `json:"breaks"`
	}
	items := []row{}
	for _, x := range rs {
		items = append(items, row{x.ID, x.Carrier, x.Filename, x.ReceivedBy, x.ReceivedAt, x.Lines, x.Matched, x.Breaks})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) settlementScan(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	n, err := s.settlement.FindUnremitted(r.Context(), wid)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]int{"unremitted_opened": n})
}

type settlementBreakJSON struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	ShipmentID     string     `json:"shipment_id,omitempty"`
	Tracking       string     `json:"tracking,omitempty"`
	Carrier        string     `json:"carrier,omitempty"`
	CourierRef     string     `json:"courier_ref,omitempty"`
	RemittanceID   string     `json:"remittance_id,omitempty"`
	ExpectedMinor  int64      `json:"expected_minor"`
	ActualMinor    int64      `json:"actual_minor"`
	DeltaMinor     int64      `json:"delta_minor"`
	Detail         string     `json:"detail,omitempty"`
	State          string     `json:"state"`
	Resolution     string     `json:"resolution,omitempty"`
	WriteoffReason string     `json:"writeoff_reason,omitempty"`
	Note           string     `json:"note,omitempty"`
	ResolvedBy     string     `json:"resolved_by,omitempty"`
	OpenedAt       time.Time  `json:"opened_at"`
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
}

func toSettlementBreak(b settlement.Break) settlementBreakJSON {
	return settlementBreakJSON{b.ID, b.Kind, b.ShipmentID, b.Tracking, b.Carrier, b.CourierRef, b.RemittanceID, b.Expected, b.Actual, b.Delta,
		b.Detail, b.State, b.Resolution, b.WriteoffReason, b.Note, b.ResolvedBy, b.OpenedAt, b.ClosedAt}
}

func (s *Server) listSettlementBreaks(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	cur, err := decodeCursor(r.URL.Query().Get("cursor"), 2)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	var after time.Time
	if cur[0] != "" {
		after, _ = time.Parse(time.RFC3339Nano, cur[0])
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "unclosed"
	}
	limit := limitParam(r)
	bs, err := s.settlement.ListBreaks(r.Context(), wid, state, r.URL.Query().Get("kind"), after, cur[1], limit+1)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	out := page[settlementBreakJSON]{Items: []settlementBreakJSON{}}
	for i, b := range bs {
		if i == limit {
			out.NextCursor = encodeCursor(bs[limit-1].OpenedAt.Format(time.RFC3339Nano), bs[limit-1].ID)
			break
		}
		out.Items = append(out.Items, toSettlementBreak(b))
	}
	writeJSON(w, 200, out)
}

func (s *Server) resolveSettlementBreak(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	var in struct {
		Resolution     string `json:"resolution"`
		Note           string `json:"note"`
		WriteoffReason string `json:"writeoff_reason"`
	}
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	if err := s.settlement.Resolve(r.Context(), wid, r.PathValue("id"), in.Resolution, in.Note, in.WriteoffReason, p.User); err != nil {
		s.problem(w, r, err)
		return
	}
	b, err := s.settlement.GetBreak(r.Context(), wid, r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, 200, toSettlementBreak(b))
}

func (s *Server) settlementSummary(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	sm, err := s.settlement.Summary(r.Context(), wid)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]int64{
		"cod_delivered_minor": sm.CODDelivered, "remitted_minor": sm.Remitted, "fees_deducted_minor": sm.FeesDeducted,
		"written_off_minor": sm.WrittenOff, "recovered_minor": sm.Recovered, "shipping_cost_lost_minor": sm.ShippingCostLost,
		"claims_owed_minor": sm.ClaimsOwed, "receivable_minor": sm.Receivable,
	})
}

// ---------------------------------------------------------------------------
// Returns
// ---------------------------------------------------------------------------

type returnLineJSON struct {
	ID          string `json:"id"`
	SKU         string `json:"sku"`
	Lot         string `json:"lot"`
	Qty         int64  `json:"qty"`
	Condition   string `json:"condition"`
	Location    string `json:"location"`
	State       string `json:"state"`
	Decision    string `json:"decision,omitempty"`
	Destination string `json:"destination,omitempty"`
}

type returnJSON struct {
	ID         string           `json:"id"`
	ShipmentID string           `json:"shipment_id"`
	OrderID    string           `json:"order_id"`
	Reason     string           `json:"reason"`
	State      string           `json:"state"`
	Tracking   string           `json:"tracking"`
	Carrier    string           `json:"carrier"`
	CreatedAt  time.Time        `json:"created_at"`
	ReceivedAt *time.Time       `json:"received_at,omitempty"`
	Lines      []returnLineJSON `json:"lines"`
}

func toReturn(x returns.RA) returnJSON {
	j := returnJSON{x.ID, x.ShipmentID, x.OrderID, x.Reason, x.State, x.Tracking, x.Carrier, x.CreatedAt, x.ReceivedAt, []returnLineJSON{}}
	for _, l := range x.Lines {
		j.Lines = append(j.Lines, returnLineJSON{l.ID, l.SKU, l.Lot, l.Qty, l.Condition, l.Location, l.State, l.Decision, l.Destination})
	}
	return j
}

func (s *Server) listReturns(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	cur, err := decodeCursor(r.URL.Query().Get("cursor"), 2)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	var before time.Time
	if cur[0] != "" {
		before, _ = time.Parse(time.RFC3339Nano, cur[0])
	}
	limit := limitParam(r)
	ras, err := s.returns.List(r.Context(), wid, r.URL.Query().Get("state"), before, cur[1], limit+1)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	out := page[returnJSON]{Items: []returnJSON{}}
	for i, x := range ras {
		if i == limit {
			out.NextCursor = encodeCursor(ras[limit-1].CreatedAt.Format(time.RFC3339Nano), ras[limit-1].ID)
			break
		}
		out.Items = append(out.Items, toReturn(x))
	}
	writeJSON(w, 200, out)
}

func (s *Server) lookupReturn(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	x, err := s.returns.ByTracking(r.Context(), wid, r.URL.Query().Get("tracking"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, 200, toReturn(x))
}

// executeReturn handles the return_receive and disposition scanner commands.
func (s *Server) executeReturn(ctx context.Context, p auth.Principal, wid inventory.WarehouseID, typ, key string, strict func(any) error) (any, error) {
	if typ == "return_receive" {
		var in struct {
			Tracking string `json:"tracking"`
			Lines    []struct {
				SKU       string `json:"sku"`
				Lot       string `json:"lot"`
				Qty       int64  `json:"qty"`
				Condition string `json:"condition"`
			} `json:"lines"`
		}
		if err := strict(&in); err != nil {
			return nil, err
		}
		if key == "" {
			return nil, apperr.Invalid("an idempotency key is required")
		}
		lines := make([]returns.IntakeLine, len(in.Lines))
		for i, l := range in.Lines {
			lines[i] = returns.IntakeLine{SKU: l.SKU, Lot: l.Lot, Qty: l.Qty, Condition: l.Condition}
		}
		rc, err := s.returns.Receive(ctx, wid, p.User, in.Tracking, key, lines)
		if err != nil {
			return nil, err
		}
		return map[string]any{"return_authorisation_id": rc.RAID, "lines": rc.Lines, "disposition_tasks": rc.Tasks, "duplicate": rc.Duplicate}, nil
	}
	var in struct {
		TaskID      string `json:"task_id"`
		Decision    string `json:"decision"`
		Destination string `json:"destination"`
	}
	if err := strict(&in); err != nil {
		return nil, err
	}
	if err := s.checkTaskOwner(ctx, p, wid, in.TaskID); err != nil {
		return nil, err
	}
	if err := s.returns.Disposition(ctx, wid, in.TaskID, in.Decision, in.Destination); err != nil {
		return nil, err
	}
	return map[string]string{"task_id": in.TaskID}, nil
}
