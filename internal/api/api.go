// Package api is the HTTP layer: routing, auth, idempotency, problem+json
// errors and cursor pagination. Handlers live in handlers.go.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/counting"
	"github.com/nicholasgasior/crossdock/internal/delivery"
	"github.com/nicholasgasior/crossdock/internal/inbound"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbound"
	"github.com/nicholasgasior/crossdock/internal/reconcile"
	"github.com/nicholasgasior/crossdock/internal/returns"
	"github.com/nicholasgasior/crossdock/internal/settlement"
	"github.com/nicholasgasior/crossdock/internal/slotting"
	"github.com/nicholasgasior/crossdock/internal/tasks"
	"github.com/nicholasgasior/crossdock/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const maxBody = 1 << 20 // 1 MiB

// Server wires the domain services to HTTP.
type Server struct {
	metrics metrics
	db      *sql.DB
	store   *inventory.Store
	inbound *inbound.Service
	out     *outbound.Service
	auth    *auth.Authenticator
	log     *slog.Logger
	spec    []byte // api/openapi.yaml, served at /v1/openapi.yaml
	// delivery is nil when webhook delivery is not configured.
	delivery  *delivery.Service
	counting  *counting.Service
	reconcile *reconcile.Service
	returns    *returns.Service
	slotting   *slotting.Service
	settlement *settlement.Service
}

// Option customises a Server.
type Option func(*Server)

// WithDelivery enables webhook endpoint management.
func WithDelivery(d *delivery.Service) Option { return func(s *Server) { s.delivery = d } }

// New builds a Server. spec may be nil.
func New(db *sql.DB, a *auth.Authenticator, log *slog.Logger, spec []byte, opts ...Option) *Server {
	store := inventory.NewStore(db)
	s := &Server{db: db, store: store, inbound: inbound.NewService(db, store),
		out: outbound.NewService(db, store), auth: a, log: log, spec: spec,
		counting: counting.NewService(db, store), reconcile: reconcile.NewService(db),
		returns: returns.NewService(db, store), settlement: settlement.NewService(db),
		slotting: slotting.NewService(db, store)}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Route declares one endpoint and who may call it.
type Route struct {
	Method, Pattern string
	// MinRole is the lowest user role allowed. Empty means no user role may call it.
	MinRole auth.Role
	// Scope, when set, also admits external-system tokens carrying it.
	Scope string
	// Secret marks responses that carry a one-time secret. They are never
	// stored for idempotent replay.
	Secret bool
	// MaxBody overrides the default request body limit (bytes). Zero means the default.
	MaxBody int64
	Handler func(s *Server, w http.ResponseWriter, r *http.Request, p auth.Principal)
}

type ctxKey struct{}

// Handler returns the full http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.db.PingContext(r.Context()); err != nil {
			s.problem(w, r, errors.New("database unavailable"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /metrics", s.metricsHandler) // scraped inside the Compose network; the proxy does not route it
	mux.HandleFunc("GET /v1/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(s.spec)
	})
	for _, rt := range Routes() {
		mux.HandleFunc(rt.Method+" "+rt.Pattern, s.wrap(rt))
	}
	return s.recoverer(mux)
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("panic in handler", "path", r.URL.Path, "panic", v)
				s.problem(w, r, errors.New("internal error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) wrap(rt Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		limit := int64(maxBody)
		if rt.MaxBody > 0 {
			limit = rt.MaxBody
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		rec := &recorder{ResponseWriter: w, status: 200}
		defer func() { s.metrics.observe(rt.Method+" "+rt.Pattern, rec.status, time.Since(start)) }()

		// The span continues the caller's trace (the scanner sends traceparent), and the
		// outbox rows written below capture it, so one trace reaches the webhook.
		ctx, span := telemetry.Tracer().Start(telemetry.Extract(r.Context(), r.Header.Get), rt.Method+" "+rt.Pattern,
			trace.WithSpanKind(trace.SpanKindServer))
		defer func() { span.SetAttributes(attribute.Int("http.status_code", rec.status)); span.End() }()
		r = r.WithContext(ctx)

		p, err := s.auth.Authenticate(r)
		if err != nil {
			s.problem(rec, r, &httpErr{http.StatusUnauthorized, "unauthenticated", err.Error()})
			s.logReq(r, rec, "", start)
			return
		}
		if !allowed(rt, p) {
			s.problem(rec, r, &httpErr{http.StatusForbidden, "forbidden", "your role or token scope does not permit this"})
			s.logReq(r, rec, p.ID(), start)
			return
		}
		if key := r.Header.Get("Idempotency-Key"); key != "" && r.Method != http.MethodGet && !rt.Secret {
			s.withIdempotency(rec, r, p, key, func() { rt.Handler(s, rec, r, p) })
		} else {
			rt.Handler(s, rec, r, p)
		}
		s.logReq(r, rec, p.ID(), start)
	}
}

func allowed(rt Route, p auth.Principal) bool {
	if p.System != "" {
		return rt.Scope != "" && p.HasScope(rt.Scope)
	}
	return rt.MinRole != "" && p.Role.AtLeast(rt.MinRole)
}

func (s *Server) logReq(r *http.Request, rec *recorder, who string, start time.Time) {
	// Caller-supplied values go in fields, never in the message.
	s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
		"principal", who, "ms", time.Since(start).Milliseconds())
}

// withIdempotency replays the stored response for a repeated Idempotency-Key,
// and stores the response of the first successful or client-error attempt.
func (s *Server) withIdempotency(w *recorder, r *http.Request, p auth.Principal, key string, run func()) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.problem(w, r, &httpErr{http.StatusRequestEntityTooLarge, "body too large", err.Error()})
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), body...))
	sig := hex.EncodeToString(sum[:])

	var status int
	var stored []byte
	var storedSig string
	err = s.db.QueryRowContext(r.Context(),
		`SELECT request_sig, status, response FROM idempotency_keys WHERE scope=$1 AND key=$2`, p.ID(), key).
		Scan(&storedSig, &status, &stored)
	if err == nil {
		if storedSig != sig {
			s.problem(w, r, &httpErr{http.StatusUnprocessableEntity, "idempotency key reuse",
				"this Idempotency-Key was used with a different request"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Idempotent-Replayed", "true")
		w.WriteHeader(status)
		_, _ = w.Write(stored)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.problem(w, r, err)
		return
	}
	run()
	if w.status < 500 { // never pin a server error: the retry should run again
		_, _ = s.db.ExecContext(context.WithoutCancel(r.Context()), `
			INSERT INTO idempotency_keys (key, scope, request_sig, status, response) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT DO NOTHING`, key, p.ID(), sig, w.status, w.body.Bytes())
	}
}

// recorder captures status and body so they can be stored for replay.
type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }
func (r *recorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

// ---------------------------------------------------------------------------
// Responses
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type httpErr struct {
	status int
	title  string
	detail string
}

func (e *httpErr) Error() string { return e.title + ": " + e.detail }

// problem writes an RFC 9457 problem details response.
func (s *Server) problem(w http.ResponseWriter, r *http.Request, err error) {
	he := toHTTPErr(err)
	if he.status >= 500 {
		s.log.Error("request failed", "path", r.URL.Path, "err", err)
		he = &httpErr{he.status, "internal error", "something went wrong; the request can be retried"}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(he.status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "about:blank", "title": he.title, "status": he.status, "detail": he.detail,
	})
}

// extraErrors lets a domain file map its own errors to status codes.
var extraErrors []func(error) *httpErr

func toHTTPErr(err error) *httpErr {
	var he *httpErr
	for _, f := range extraErrors {
		if h := f(err); h != nil {
			return h
		}
	}
	switch {
	case errors.As(err, &he):
		return he
	case errors.Is(err, apperr.ErrInvalid):
		return &httpErr{http.StatusUnprocessableEntity, "invalid request", strings.TrimPrefix(err.Error(), apperr.ErrInvalid.Error()+": ")}
	case errors.Is(err, inbound.ErrNotFound), errors.Is(err, outbound.ErrNotFound), errors.Is(err, tasks.ErrNotFound):
		return &httpErr{http.StatusNotFound, "not found", "the requested resource does not exist"}
	case errors.Is(err, tasks.ErrNotAssignee):
		return &httpErr{http.StatusForbidden, "not your task", "this task is assigned to someone else"}
	case errors.Is(err, inventory.ErrMovementConflict):
		return &httpErr{http.StatusConflict, "stock conflict", "the stock is no longer available for this action"}
	case errors.Is(err, inbound.ErrLocationFull):
		return &httpErr{http.StatusConflict, "location full", "the destination has no room; raise an exception or pick another location"}
	case errors.Is(err, outbound.ErrNotCancellable):
		return &httpErr{http.StatusConflict, "not cancellable", "stock is already picked"}
	case errors.Is(err, outbound.ErrBadState), errors.Is(err, tasks.ErrBadTransition),
		errors.Is(err, inbound.ErrWrongTask), errors.Is(err, outbound.ErrWrongTask):
		return &httpErr{http.StatusConflict, "wrong state", err.Error()}
	case errors.Is(err, outbound.ErrNoPackStation):
		return &httpErr{http.StatusConflict, "no packing station", err.Error()}
	}
	return &httpErr{http.StatusInternalServerError, "internal error", err.Error()}
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &httpErr{http.StatusBadRequest, "bad request body", err.Error()}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Cursors: opaque base64 of "a\x1fb\x1fc..."
// ---------------------------------------------------------------------------

func encodeCursor(parts ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, "\x1f")))
}

func decodeCursor(c string, n int) ([]string, error) {
	if c == "" {
		return make([]string, n), nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(c)
	parts := strings.Split(string(raw), "\x1f")
	if err != nil || len(parts) != n {
		return nil, &httpErr{http.StatusBadRequest, "bad cursor", "the cursor is not valid"}
	}
	return parts, nil
}
