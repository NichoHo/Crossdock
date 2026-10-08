package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/counting"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/reconcile"
)

// Counting, variances, three-way reconciliation and the workbench (spec 8).

func init() {
	// Counting errors map to the same status codes as the other domains.
	extraErrors = append(extraErrors, func(err error) *httpErr {
		switch {
		case errors.Is(err, counting.ErrNotFound), errors.Is(err, reconcile.ErrNotFound):
			return &httpErr{http.StatusNotFound, "not found", "the requested resource does not exist"}
		case errors.Is(err, counting.ErrBadState):
			return &httpErr{http.StatusConflict, "wrong state", "this variance is not waiting for a decision"}
		case errors.Is(err, counting.ErrWrongTask):
			return &httpErr{http.StatusConflict, "wrong task", "that task is not a count task"}
		}
		return nil
	})
}

type varianceJSON struct {
	ID             string          `json:"id"`
	CountID        string          `json:"count_id"`
	Location       string          `json:"location"`
	SKU            string          `json:"sku"`
	Lot            string          `json:"lot"`
	LPN            string          `json:"lpn,omitempty"`
	SystemQty      int64           `json:"system_qty"`
	CountedQty     int64           `json:"counted_qty"`
	DamagedQty     int64           `json:"damaged_qty,omitempty"`
	Variance       int64           `json:"variance"`
	InFlight       json.RawMessage `json:"in_flight,omitempty"`
	Proposed       string          `json:"proposed"`
	Classification string          `json:"classification,omitempty"`
	State          string          `json:"state"`
	Flagged        bool            `json:"flagged,omitempty"`
	RecountTaskID  string          `json:"recount_task_id,omitempty"`
	OpenedAt       time.Time       `json:"opened_at"`
	ClosedAt       *time.Time      `json:"closed_at,omitempty"`
	ConfirmedBy    string          `json:"confirmed_by,omitempty"`
	Note           string          `json:"note,omitempty"`
	Adjustment     string          `json:"adjustment_movement_id,omitempty"`
}

func toVariance(v counting.Variance, detail bool) varianceJSON {
	j := varianceJSON{v.ID, v.CountID, v.Location, v.SKU, v.Lot, v.LPN, v.SystemQty, v.CountedQty, v.DamagedQty, v.Variance, nil,
		string(v.Proposed), string(v.Classification), v.State, v.Flagged, v.RecountTaskID, v.OpenedAt, v.ClosedAt, v.ConfirmedBy, v.Note, v.AdjustmentMovement}
	if detail {
		j.InFlight = v.InFlight
	}
	return j
}

func (s *Server) scheduleCounts(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	n, err := s.counting.ScheduleCounts(r.Context(), wid)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]int{"created": n})
}

func (s *Server) countSheet(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	lines, err := s.counting.Sheet(r.Context(), wid, r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	type item struct {
		SKU string `json:"sku"`
		Lot string `json:"lot"`
		LPN string `json:"lpn,omitempty"`
	}
	items := []item{}
	for _, l := range lines {
		items = append(items, item{l.SKU, l.Lot, l.LPN})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) listVariances(w http.ResponseWriter, r *http.Request, p auth.Principal) {
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
	vs, err := s.counting.ListVariances(r.Context(), wid, state, after, cur[1], limit+1)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	out := page[varianceJSON]{Items: []varianceJSON{}}
	for i, v := range vs {
		if i == limit {
			out.NextCursor = encodeCursor(vs[limit-1].OpenedAt.Format(time.RFC3339Nano), vs[limit-1].ID)
			break
		}
		out.Items = append(out.Items, toVariance(v, false))
	}
	writeJSON(w, 200, out)
}

func (s *Server) getVariance(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	v, err := s.counting.GetVariance(r.Context(), wid, r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, 200, toVariance(v, true))
}

func (s *Server) confirmVariance(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	var in struct {
		Classification string `json:"classification"`
		Note           string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	actor := p.User
	if err := s.counting.Confirm(r.Context(), wid, r.PathValue("id"), counting.Class(in.Classification), in.Note, actor); err != nil {
		s.problem(w, r, err)
		return
	}
	v, err := s.counting.GetVariance(r.Context(), wid, r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, 200, toVariance(v, true))
}

type breakJSON struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	ASNID     string     `json:"asn_id"`
	ASNRef    string     `json:"asn_ref"`
	Supplier  string     `json:"supplier,omitempty"`
	LineID    string     `json:"asn_line_id"`
	SKU       string     `json:"sku"`
	Expected  int64      `json:"expected_qty"`
	Received  int64      `json:"received_qty"`
	Putaway   int64      `json:"putaway_qty"`
	Detail    string     `json:"detail,omitempty"`
	State     string     `json:"state"`
	Note      string     `json:"note,omitempty"`
	ClosedBy  string     `json:"closed_by,omitempty"`
	OpenedAt  time.Time  `json:"opened_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
}

func (s *Server) listBreaks(w http.ResponseWriter, r *http.Request, p auth.Principal) {
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
	bs, err := s.reconcile.List(r.Context(), wid, state, r.URL.Query().Get("kind"), after, cur[1], limit+1)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	out := page[breakJSON]{Items: []breakJSON{}}
	for i, b := range bs {
		if i == limit {
			out.NextCursor = encodeCursor(bs[limit-1].OpenedAt.Format(time.RFC3339Nano), bs[limit-1].ID)
			break
		}
		out.Items = append(out.Items, breakJSON{b.ID, b.Kind, b.ASNID, b.ASNRef, b.Supplier, b.ASNLineID, b.SKU, b.Expected, b.Received,
			b.Putaway, b.Detail, b.State, b.Note, b.ClosedBy, b.OpenedAt, b.ClosedAt})
	}
	writeJSON(w, 200, out)
}

func (s *Server) waiveBreak(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	if err := s.reconcile.Waive(r.Context(), wid, r.PathValue("id"), in.Note, p.User); err != nil {
		s.problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) runReconciliation(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	o, c, err := s.reconcile.Run(r.Context(), wid)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]int{"opened": o, "cleared": c})
}

// workbench is the manager's number: open variances and breaks by age. The
// over-90 bucket is what a warehouse manager is judged on (spec 8.4).
func (s *Server) workbench(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	v, err := s.counting.Ageing(r.Context(), wid)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	b, err := s.reconcile.Ageing(r.Context(), wid)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	sc, err := s.settlement.Ageing(r.Context(), wid)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	total := counting.Ageing{Under7: v.Under7 + b.Under7 + sc.Under7, D7to30: v.D7to30 + b.D7to30 + sc.D7to30,
		D30to90: v.D30to90 + b.D30to90 + sc.D30to90, Over90: v.Over90 + b.Over90 + sc.Over90}
	writeJSON(w, 200, map[string]any{"variances": v, "breaks": b, "settlement": sc, "total": total, "open": total.Total()})
}

// ---------------------------------------------------------------------------
// Scanner commands
// ---------------------------------------------------------------------------

type countPayload struct {
	TaskID string `json:"task_id"`
	Lines  []struct {
		SKU     string `json:"sku"`
		Lot     string `json:"lot"`
		LPN     string `json:"lpn"`
		Counted int64  `json:"counted"`
		Damaged int64  `json:"damaged"`
	} `json:"lines"`
}

// executeCount handles the count_start and count commands.
func (s *Server) executeCount(ctx context.Context, p auth.Principal, wid inventory.WarehouseID, typ string, strict func(any) error) (any, error) {
	var in countPayload
	if err := strict(&in); err != nil {
		return nil, err
	}
	if err := s.checkTaskOwner(ctx, p, wid, in.TaskID); err != nil {
		return nil, err
	}
	if typ == "count_start" {
		if err := s.counting.Start(ctx, wid, in.TaskID, p.User); err != nil {
			return nil, err
		}
		return map[string]string{"task_id": in.TaskID}, nil
	}
	if len(in.Lines) == 0 {
		// Nothing reported is only believable for a shelf the system says is empty.
		sheet, err := s.counting.Sheet(ctx, wid, in.TaskID)
		if err != nil {
			return nil, err
		}
		if len(sheet) > 0 {
			return nil, apperr.Invalid("report each item on the sheet, with 0 for any you did not find")
		}
	}
	lines := make([]counting.Line, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = counting.Line{SKU: l.SKU, Lot: l.Lot, LPN: l.LPN, Counted: l.Counted, Damaged: l.Damaged}
	}
	res, err := s.counting.Submit(ctx, wid, in.TaskID, p.User, lines)
	if err != nil {
		return nil, err
	}
	return map[string]any{"count_id": res.CountID, "matched": res.Matched, "variances": len(res.Variances), "recount_task_id": res.Recount}, nil
}
