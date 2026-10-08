// Package auth implements the two ways a caller reaches the API (spec 13.1):
//
//  1. The Next.js layer presents a service token and forwards the signed-in
//     user's identity in a header signed with HMAC-SHA256.
//  2. An external system presents its own API token, hashed at rest, scoped to
//     specific actions.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Role is a user role. Higher rank includes the permissions of lower ranks.
type Role string

const (
	RoleOperator   Role = "operator"
	RoleSupervisor Role = "supervisor"
	RoleAdmin      Role = "admin"
)

func (r Role) rank() int {
	switch r {
	case RoleOperator:
		return 1
	case RoleSupervisor:
		return 2
	case RoleAdmin:
		return 3
	}
	return 0
}

// AtLeast reports whether r has the permissions of min.
func (r Role) AtLeast(min Role) bool { return r.rank() >= min.rank() && r.rank() > 0 }

// Principal is the authenticated caller.
type Principal struct {
	// User is set for human callers (via the Next.js layer).
	User string
	Role Role
	// System and Scopes are set for external-system tokens.
	System string
	Scopes []string
}

// ID is a stable string for logging and idempotency scoping.
func (p Principal) ID() string {
	if p.System != "" {
		return "system:" + p.System
	}
	return "user:" + p.User
}

// HasScope reports whether a system token carries scope.
func (p Principal) HasScope(scope string) bool {
	for _, s := range p.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

const (
	headerIdentity  = "X-Crossdock-Identity"
	headerSignature = "X-Crossdock-Signature"
	maxSkew         = 5 * time.Minute
	// TokenPrefix marks external API tokens so they are easy to spot in logs and scanners.
	TokenPrefix = "cdk_"
)

// Authenticator verifies requests.
type Authenticator struct {
	db           *sql.DB
	serviceToken []byte
	signingKey   []byte
}

// New builds an Authenticator. serviceToken and signingKey must both be at least 32 bytes.
func New(db *sql.DB, serviceToken, signingKey string) (*Authenticator, error) {
	if len(serviceToken) < 32 || len(signingKey) < 32 {
		return nil, errors.New("auth: service token and signing key must each be at least 32 characters")
	}
	return &Authenticator{db: db, serviceToken: []byte(serviceToken), signingKey: []byte(signingKey)}, nil
}

// SignIdentity returns the header values the Next.js layer sends for a user.
func SignIdentity(signingKey, user string, role Role, at time.Time) (identity, signature string) {
	payload := user + "|" + string(role) + "|" + strconv.FormatInt(at.Unix(), 10)
	identity = base64.RawURLEncoding.EncodeToString([]byte(payload))
	m := hmac.New(sha256.New, []byte(signingKey))
	m.Write([]byte(identity))
	return identity, hex.EncodeToString(m.Sum(nil))
}

// Authenticate resolves the caller or returns an error (map it to 401).
func (a *Authenticator) Authenticate(r *http.Request) (Principal, error) {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || tok == "" {
		return Principal{}, errors.New("missing bearer token")
	}
	if strings.HasPrefix(tok, TokenPrefix) {
		return a.systemToken(r.Context(), tok)
	}
	if subtle.ConstantTimeCompare([]byte(tok), a.serviceToken) != 1 {
		return Principal{}, errors.New("invalid token")
	}
	return a.userIdentity(r)
}

func (a *Authenticator) userIdentity(r *http.Request) (Principal, error) {
	id, sig := r.Header.Get(headerIdentity), r.Header.Get(headerSignature)
	if id == "" || sig == "" {
		return Principal{}, errors.New("missing identity headers")
	}
	m := hmac.New(sha256.New, a.signingKey)
	m.Write([]byte(id))
	want := hex.EncodeToString(m.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(want)) != 1 {
		return Principal{}, errors.New("bad identity signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return Principal{}, errors.New("bad identity encoding")
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return Principal{}, errors.New("bad identity payload")
	}
	ts, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return Principal{}, errors.New("bad identity timestamp")
	}
	if d := time.Since(time.Unix(ts, 0)); d > maxSkew || d < -maxSkew {
		return Principal{}, errors.New("identity timestamp outside the allowed window")
	}
	role := Role(parts[1])
	if role.rank() == 0 || parts[0] == "" {
		return Principal{}, errors.New("unknown role or empty user")
	}
	return Principal{User: parts[0], Role: role}, nil
}

// HashToken returns the hex sha256 stored for an API token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (a *Authenticator) systemToken(ctx context.Context, tok string) (Principal, error) {
	var name string
	var scopes pq.StringArray
	err := a.db.QueryRowContext(ctx, `
		SELECT name, scopes FROM api_tokens WHERE token_hash=$1 AND revoked_at IS NULL`, HashToken(tok)).Scan(&name, &scopes)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, errors.New("invalid token")
	}
	if err != nil {
		return Principal{}, fmt.Errorf("token lookup: %w", err)
	}
	return Principal{System: name, Scopes: scopes}, nil
}

// CreateToken mints a new external API token. The plaintext is returned once;
// only its hash is stored.
func CreateToken(ctx context.Context, db *sql.DB, name string, scopes []string) (id, plaintext string, err error) {
	var b [24]byte
	if _, err = rand.Read(b[:]); err != nil {
		return
	}
	plaintext = TokenPrefix + hex.EncodeToString(b[:])
	id = "tok-" + hex.EncodeToString(b[:4])
	_, err = db.ExecContext(ctx, `INSERT INTO api_tokens (id, name, token_hash, scopes) VALUES ($1,$2,$3,$4)`,
		id, name, HashToken(plaintext), pq.Array(scopes))
	return
}
