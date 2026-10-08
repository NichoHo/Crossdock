package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/slotting"
)

// Slotting proposals, the floor map and relocate tasks (spec 10, 12.5).

func init() {
	extraErrors = append(extraErrors, func(err error) *httpErr {
		switch {
		case errors.Is(err, slotting.ErrNotFound):
			return &httpErr{http.StatusNotFound, "not found", "no such proposal"}
		case errors.Is(err, slotting.ErrWrongTask):
			return &httpErr{http.StatusConflict, "wrong task", "that task is not a relocate task"}
		}
		return nil
	})
}

type moveJSON struct {
	Seq    int    `json:"seq"`
	SKU    string `json:"sku"`
	From   string `json:"from_location"`
	To     string `json:"to_location"`
	Reason string `json:"reason,omitempty"`
}

type realisedJSON struct {
	WavesBefore int        `json:"waves_before"`
	WavesAfter  int        `json:"waves_after"`
	AvgBeforeM  float64    `json:"avg_before_m"`
	AvgAfterM   float64    `json:"avg_after_m"`
	Since       *time.Time `json:"since,omitempty"`
}

type proposalJSON struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	CreatedAt  time.Time  `json:"created_at"`
	WindowDays int        `json:"window_days"`
	MaxMoves   int        `json:"max_moves"`
	MoveCount  int        `json:"move_count"`
	Solver     string     `json:"solver"`
	// The saving is what the platform measured by replaying the waves with its own
	// router. It is absent until the proposal has been replayed.
	ReplayWaves   *int     `json:"replay_waves,omitempty"`
	ReplayBeforeM *float64 `json:"replay_before_m,omitempty"`
	ReplayAfterM  *float64 `json:"replay_after_m,omitempty"`
	SavingM       *float64 `json:"saving_m,omitempty"`
	SavingPct     *float64 `json:"saving_pct,omitempty"`
	// SolverEstimate is the solver's own proxy objective, kept for reference only.
	SolverEstimate map[string]float64 `json:"solver_estimate"`
	AcceptedBy     string             `json:"accepted_by,omitempty"`
	AcceptedAt     *time.Time         `json:"accepted_at,omitempty"`
	Note           string             `json:"note,omitempty"`
	Moves          []moveJSON         `json:"moves,omitempty"`
	Realised       *realisedJSON      `json:"realised,omitempty"`
}

func toProposal(p slotting.Proposal) proposalJSON {
	j := proposalJSON{ID: p.ID, State: p.State, CreatedAt: p.CreatedAt, WindowDays: p.WindowDays, MaxMoves: p.MaxMoves, MoveCount: len(p.Moves),
		Solver: p.Solver, ReplayWaves: p.ReplayWaves, ReplayBeforeM: p.ReplayBeforeM, ReplayAfterM: p.ReplayAfterM,
		SolverEstimate: map[string]float64{"before": p.ObjectiveBefore, "after": p.ObjectiveAfter},
		AcceptedBy:     p.AcceptedBy, AcceptedAt: p.AcceptedAt, Note: p.Note}
	if sv := p.SavingM(); sv != nil {
		j.SavingM = sv
		if *p.ReplayBeforeM > 0 {
			pct := 100 * *sv / *p.ReplayBeforeM
			j.SavingPct = &pct
		}
	}
	for _, m := range p.Moves {
		j.Moves = append(j.Moves, moveJSON{m.Seq, m.SKU, m.From, m.To, m.Reason})
	}
	return j
}

func (s *Server) listProposals(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	ps, err := s.slotting.List(r.Context(), wid, limitParam(r))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	items := []proposalJSON{}
	for _, x := range ps {
		full, err := s.slotting.Get(r.Context(), wid, x.ID) // the list needs the move count
		if err != nil {
			s.problem(w, r, err)
			return
		}
		j := toProposal(full)
		j.Moves = nil
		items = append(items, j)
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) getProposal(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	pr, err := s.slotting.Get(r.Context(), wid, r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	j := toProposal(pr)
	if re, err := s.slotting.Realised(r.Context(), wid, pr); err == nil && re.Since != nil {
		j.Realised = &realisedJSON{re.WavesBefore, re.WavesAfter, re.AvgBeforeM, re.AvgAfterM, re.Since}
	}
	writeJSON(w, 200, j)
}

func (s *Server) replayProposal(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	if _, err := s.slotting.Replay(r.Context(), wid, r.PathValue("id")); err != nil {
		s.problem(w, r, err)
		return
	}
	s.getProposal(w, r, p)
}

func (s *Server) acceptProposal(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	n, err := s.slotting.Accept(r.Context(), wid, r.PathValue("id"), p.User)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"relocate_tasks": n})
}

func (s *Server) rejectProposal(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			s.problem(w, r, err)
			return
		}
	}
	if err := s.slotting.Reject(r.Context(), wid, r.PathValue("id"), in.Note); err != nil {
		s.problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) floorMap(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	cells, err := s.slotting.FloorMap(r.Context(), wid, days)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	type cell struct {
		Code  string `json:"code"`
		Zone  string `json:"zone"`
		Picks int    `json:"picks"`
		Units int64  `json:"units"`
	}
	items := []cell{}
	for _, c := range cells {
		items = append(items, cell{c.Code, c.Zone, c.Picks, c.Units})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// executeRelocate is the scanner's relocate command.
func (s *Server) executeRelocate(ctx context.Context, p auth.Principal, wid inventory.WarehouseID, strict func(any) error) (any, error) {
	var in struct {
		TaskID string `json:"task_id"`
	}
	if err := strict(&in); err != nil {
		return nil, err
	}
	if err := s.checkTaskOwner(ctx, p, wid, in.TaskID); err != nil {
		return nil, err
	}
	if err := s.slotting.CompleteRelocate(ctx, wid, in.TaskID); err != nil {
		return nil, err
	}
	return map[string]string{"task_id": in.TaskID}, nil
}
