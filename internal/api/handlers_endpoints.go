package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/delivery"
)

// Webhook endpoint management (spec 7.3). Routes are registered in Routes().

type endpointJSON struct {
	ID                  string     `json:"id"`
	URL                 string     `json:"url"`
	Events              []string   `json:"events"`
	State               string     `json:"state"`
	Breaker             string     `json:"breaker"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	OpenUntil           *time.Time `json:"open_until,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	Pending             *int       `json:"pending,omitempty"`
	Delivered           *int       `json:"delivered,omitempty"`
	DeadLetters         *int       `json:"dead_letters,omitempty"`
}

func toEndpoint(e delivery.Endpoint) endpointJSON {
	return endpointJSON{e.ID, e.URL, e.Events, e.State, e.Breaker, e.ConsecutiveFailures, e.OpenUntil, e.CreatedAt, nil, nil, nil}
}

// deliveryOK writes a 503 when webhook delivery is not configured.
func (s *Server) deliveryOK(w http.ResponseWriter, r *http.Request) bool {
	if s.delivery == nil {
		s.problem(w, r, &httpErr{http.StatusServiceUnavailable, "webhooks not configured",
			"set CROSSDOCK_SECRET_ENCRYPTION_KEY to enable webhook delivery"})
		return false
	}
	return true
}

func (s *Server) createEndpoint(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok || !s.deliveryOK(w, r) {
		return
	}
	var in struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
	}
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	ep, secret, err := s.delivery.Create(r.Context(), string(wid), in.URL, in.Events)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	// The secret appears in this response only. It is stored hashed and encrypted.
	writeJSON(w, http.StatusCreated, map[string]any{"endpoint": toEndpoint(ep), "secret": secret})
}

func (s *Server) listEndpoints(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok || !s.deliveryOK(w, r) {
		return
	}
	limit := limitParam(r)
	eps, err := s.delivery.List(r.Context(), string(wid), r.URL.Query().Get("cursor"), limit+1)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	out := page[endpointJSON]{Items: []endpointJSON{}}
	for i, e := range eps {
		if i == limit {
			out.NextCursor = eps[limit-1].ID
			break
		}
		j := toEndpoint(e)
		st, err := s.delivery.Stats(r.Context(), string(wid), e.ID)
		if err != nil {
			s.problem(w, r, err)
			return
		}
		j.Pending, j.Delivered, j.DeadLetters = &st.Pending, &st.Delivered, &st.Dead
		out.Items = append(out.Items, j)
	}
	writeJSON(w, 200, out)
}

func (s *Server) getEndpoint(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok || !s.deliveryOK(w, r) {
		return
	}
	e, err := s.delivery.Get(r.Context(), string(wid), r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	st, err := s.delivery.Stats(r.Context(), string(wid), e.ID)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	j := toEndpoint(e)
	j.Pending, j.Delivered, j.DeadLetters = &st.Pending, &st.Delivered, &st.Dead
	writeJSON(w, 200, j)
}

func (s *Server) disableEndpoint(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok || !s.deliveryOK(w, r) {
		return
	}
	if err := s.delivery.Disable(r.Context(), string(wid), r.PathValue("id")); err != nil {
		s.problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) rotateSecret(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok || !s.deliveryOK(w, r) {
		return
	}
	var in struct {
		OverlapHours int `json:"overlap_hours"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			s.problem(w, r, err)
			return
		}
	}
	if in.OverlapHours == 0 {
		in.OverlapHours = 24
	}
	if in.OverlapHours < 1 || in.OverlapHours > 24*30 {
		s.problem(w, r, apperr.Invalid("overlap_hours must be between 1 and 720"))
		return
	}
	secret, err := s.delivery.Rotate(r.Context(), string(wid), r.PathValue("id"), time.Duration(in.OverlapHours)*time.Hour)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"secret": secret, "previous_valid_hours": in.OverlapHours})
}

type deliveryJSON struct {
	ID          string    `json:"id"`
	EventID     string    `json:"event_id"`
	Topic       string    `json:"topic"`
	State       string    `json:"state"`
	Attempts    int       `json:"attempts"`
	NextAttempt time.Time `json:"next_attempt_at"`
	LastStatus  int       `json:"last_status"`
	LastError   string    `json:"last_error,omitempty"`
	ReplayOf    string    `json:"replay_of,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok || !s.deliveryOK(w, r) {
		return
	}
	cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	limit := limitParam(r)
	ds, err := s.delivery.ListDeliveries(r.Context(), string(wid), r.PathValue("id"), r.URL.Query().Get("state"), cursor, limit+1)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	out := page[deliveryJSON]{Items: []deliveryJSON{}}
	for i, d := range ds {
		if i == limit {
			out.NextCursor = strconv.FormatInt(ds[limit-1].EventID, 10)
			break
		}
		out.Items = append(out.Items, deliveryJSON{d.ID, "evt_" + strconv.FormatInt(d.EventID, 10), d.Topic, d.State,
			d.Attempts, d.NextAttempt, d.LastStatus, d.LastError, d.ReplayOf, d.CreatedAt})
	}
	writeJSON(w, 200, out)
}

func (s *Server) deliveryAttempts(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok || !s.deliveryOK(w, r) {
		return
	}
	as, err := s.delivery.Attempts(r.Context(), string(wid), r.PathValue("did"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	type attempt struct {
		N          int       `json:"n"`
		At         time.Time `json:"at"`
		Status     int       `json:"status"`
		Error      string    `json:"error,omitempty"`
		Response   string    `json:"response,omitempty"`
		DurationMS int       `json:"duration_ms"`
	}
	items := []attempt{}
	for _, a := range as {
		items = append(items, attempt{a.N, a.At, a.Status, a.Error, a.Response, a.DurationMS})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// replay redelivers one event, every unreplayed dead letter, or a time range.
func (s *Server) replay(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok || !s.deliveryOK(w, r) {
		return
	}
	var in struct {
		EventID     string     `json:"event_id"`
		DeadLetters bool       `json:"dead_letters"`
		From        *time.Time `json:"from"`
		To          *time.Time `json:"to"`
	}
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	ep := r.PathValue("id")
	var n int
	var err error
	switch {
	case in.EventID != "":
		id, perr := strconv.ParseInt(trimEvt(in.EventID), 10, 64)
		if perr != nil {
			s.problem(w, r, apperr.Invalid("event_id must look like evt_123"))
			return
		}
		n = 1
		err = s.delivery.ReplayEvent(r.Context(), string(wid), ep, id)
	case in.DeadLetters:
		n, err = s.delivery.ReplayDead(r.Context(), string(wid), ep)
	case in.From != nil && in.To != nil:
		n, err = s.delivery.ReplayRange(r.Context(), string(wid), ep, *in.From, *in.To)
	default:
		s.problem(w, r, apperr.Invalid("give event_id, dead_letters, or from and to"))
		return
	}
	if err != nil {
		if errors.Is(err, delivery.ErrNotFound) {
			err = &httpErr{http.StatusNotFound, "not found", "no such endpoint or event"}
		}
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"queued": n})
}

func trimEvt(s string) string {
	if len(s) > 4 && s[:4] == "evt_" {
		return s[4:]
	}
	return s
}
