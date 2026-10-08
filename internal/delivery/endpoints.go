package delivery

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/ids"
)

var ErrNotFound = errors.New("delivery: not found")

// Service manages endpoints, secrets, deliveries and replays.
type Service struct {
	db           *sql.DB
	enc          *Encryptor
	allowPrivate bool
	now          func() time.Time
}

func NewService(db *sql.DB, enc *Encryptor, allowPrivate bool) *Service {
	return &Service{db: db, enc: enc, allowPrivate: allowPrivate, now: time.Now}
}

type Endpoint struct {
	ID                  string
	WarehouseID         string
	URL                 string
	Events              []string
	State               string
	Breaker             string
	ConsecutiveFailures int
	OpenUntil           *time.Time
	CreatedAt           time.Time
}

// Stats counts an endpoint's deliveries.
type Stats struct {
	Pending   int
	Delivered int
	// Dead counts dead letters that have not been replayed yet.
	Dead int
}

const endpointCols = `id, warehouse_id, url, events, state, breaker, consecutive_failures, open_until, created_at`

func scanEndpoint(r interface{ Scan(...any) error }) (Endpoint, error) {
	var e Endpoint
	var ev pq.StringArray
	err := r.Scan(&e.ID, &e.WarehouseID, &e.URL, &ev, &e.State, &e.Breaker, &e.ConsecutiveFailures, &e.OpenUntil, &e.CreatedAt)
	e.Events = ev
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// Matches reports whether a topic passes an event filter: exact, "prefix.*" or "*".
func Matches(filter []string, topic string) bool {
	for _, f := range filter {
		switch {
		case f == "*" || f == topic:
			return true
		case strings.HasSuffix(f, ".*") && strings.HasPrefix(topic, strings.TrimSuffix(f, "*")):
			return true
		}
	}
	return false
}

// Create registers an endpoint and returns its signing secret. The secret is
// not retrievable afterwards.
func (s *Service) Create(ctx context.Context, wid, rawURL string, events []string) (Endpoint, string, error) {
	if _, err := ValidateURL(rawURL, s.allowPrivate); err != nil {
		return Endpoint{}, "", apperr.Invalid("%v", err)
	}
	if len(events) == 0 {
		return Endpoint{}, "", apperr.Invalid("choose at least one event, or \"*\" for all")
	}
	for _, f := range events {
		if f == "" || strings.ContainsAny(f, " ,") {
			return Endpoint{}, "", apperr.Invalid("bad event filter %q", f)
		}
	}
	secret := NewSecret()
	id := ids.New("ep")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Endpoint{}, "", err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `INSERT INTO endpoints (id, warehouse_id, url, events) VALUES ($1,$2,$3,$4)`,
		id, wid, rawURL, pq.Array(events)); err != nil {
		return Endpoint{}, "", err
	}
	if err := s.insertSecret(ctx, tx, wid, id, secret); err != nil {
		return Endpoint{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return Endpoint{}, "", err
	}
	e, err := s.Get(ctx, wid, id)
	return e, secret, err
}

func (s *Service) insertSecret(ctx context.Context, tx *sql.Tx, wid, endpointID, secret string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO endpoint_secrets (id, warehouse_id, endpoint_id, secret_enc, secret_hash) VALUES ($1,$2,$3,$4,$5)`,
		ids.New("sec"), wid, endpointID, s.enc.Seal([]byte(secret)), HashSecret(secret))
	return err
}

func (s *Service) Get(ctx context.Context, wid, id string) (Endpoint, error) {
	return scanEndpoint(s.db.QueryRowContext(ctx, `SELECT `+endpointCols+` FROM endpoints WHERE warehouse_id=$1 AND id=$2`, wid, id))
}

func (s *Service) List(ctx context.Context, wid, after string, limit int) ([]Endpoint, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+endpointCols+` FROM endpoints WHERE warehouse_id=$1 AND id > $2 ORDER BY id LIMIT $3`, wid, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Endpoint
	for rows.Next() {
		e, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Disable stops deliveries to an endpoint. Pending deliveries stay for replay.
func (s *Service) Disable(ctx context.Context, wid, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE endpoints SET state='disabled' WHERE warehouse_id=$1 AND id=$2`, wid, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Rotate issues a new secret. The previous one keeps working for overlap, so a
// receiver can switch without dropping events.
func (s *Service) Rotate(ctx context.Context, wid, id string, overlap time.Duration) (string, error) {
	secret := NewSecret()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM endpoints WHERE warehouse_id=$1 AND id=$2)`, wid, id).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return "", ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE endpoint_secrets SET expires_at = $3 WHERE warehouse_id=$1 AND endpoint_id=$2 AND expires_at IS NULL`,
		wid, id, s.now().Add(overlap)); err != nil {
		return "", err
	}
	if err := s.insertSecret(ctx, tx, wid, id, secret); err != nil {
		return "", err
	}
	return secret, tx.Commit()
}

// signingSecrets returns every secret that is currently valid, newest first.
func (s *Service) signingSecrets(ctx context.Context, wid, id string, now time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT secret_enc FROM endpoint_secrets
		WHERE warehouse_id=$1 AND endpoint_id=$2 AND (expires_at IS NULL OR expires_at > $3)
		ORDER BY created_at DESC, id DESC`, wid, id, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var enc []byte
		if err := rows.Scan(&enc); err != nil {
			return nil, err
		}
		plain, err := s.enc.Open(enc)
		if err != nil {
			return nil, err
		}
		out = append(out, string(plain))
	}
	return out, rows.Err()
}

func (s *Service) Stats(ctx context.Context, wid, id string) (Stats, error) {
	var st Stats
	err := s.db.QueryRowContext(ctx, `
		SELECT
		  count(*) FILTER (WHERE state='pending'),
		  count(*) FILTER (WHERE state='delivered' AND replay_of=''),
		  count(*) FILTER (WHERE state='dead' AND replay_of=''
		       AND NOT EXISTS (SELECT 1 FROM deliveries r WHERE r.replay_of = deliveries.id))
		FROM deliveries WHERE warehouse_id=$1 AND endpoint_id=$2`, wid, id).Scan(&st.Pending, &st.Delivered, &st.Dead)
	return st, err
}

// Delivery is one delivery with its last outcome.
type Delivery struct {
	ID          string
	EventID     int64
	Topic       string
	State       string
	Attempts    int
	NextAttempt time.Time
	LastStatus  int
	LastError   string
	ReplayOf    string
	CreatedAt   time.Time
}

// Attempt is one try, kept for dead letters.
type Attempt struct {
	N          int
	At         time.Time
	Status     int
	Error      string
	Response   string
	DurationMS int
}

// ListDeliveries pages an endpoint's deliveries newest first. cursor is the
// last event id of the previous page (0 for the first page).
func (s *Service) ListDeliveries(ctx context.Context, wid, endpointID, state string, cursor int64, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if cursor == 0 {
		cursor = 1<<62 - 1
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.event_id, ev.topic, d.state, d.attempts, d.next_attempt_at, d.last_status, d.last_error, d.replay_of, d.created_at
		FROM deliveries d JOIN events ev ON ev.id = d.event_id
		WHERE d.warehouse_id=$1 AND d.endpoint_id=$2 AND ($3='' OR d.state=$3) AND d.event_id < $4
		ORDER BY d.event_id DESC, d.created_at DESC LIMIT $5`, wid, endpointID, state, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.EventID, &d.Topic, &d.State, &d.Attempts, &d.NextAttempt, &d.LastStatus, &d.LastError, &d.ReplayOf, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Attempts returns every recorded attempt of a delivery, oldest first.
func (s *Service) Attempts(ctx context.Context, wid, deliveryID string) ([]Attempt, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.n, a.at, a.status, a.error, a.response, a.duration_ms
		FROM delivery_attempts a JOIN deliveries d ON d.id = a.delivery_id
		WHERE d.warehouse_id=$1 AND a.delivery_id=$2 ORDER BY a.n`, wid, deliveryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attempt
	for rows.Next() {
		var a Attempt
		if err := rows.Scan(&a.N, &a.At, &a.Status, &a.Error, &a.Response, &a.DurationMS); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
