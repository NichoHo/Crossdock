package delivery

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Encryptor seals endpoint secrets at rest with AES-256-GCM.
type Encryptor struct{ gcm cipher.AEAD }

// NewEncryptor takes a base64 encoded 32 byte key.
func NewEncryptor(b64Key string) (*Encryptor, error) {
	key, err := base64.StdEncoding.DecodeString(b64Key)
	if err != nil || len(key) != 32 {
		return nil, errors.New("delivery: encryption key must be 32 bytes, base64 encoded")
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &Encryptor{gcm: g}, nil
}

func (e *Encryptor) Seal(plain []byte) []byte {
	nonce := make([]byte, e.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return e.gcm.Seal(nonce, nonce, plain, nil)
}

func (e *Encryptor) Open(sealed []byte) ([]byte, error) {
	n := e.gcm.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("delivery: sealed secret too short")
	}
	return e.gcm.Open(nil, sealed[:n], sealed[n:], nil)
}

// NewSecret returns a fresh signing secret (shown to the user once).
func NewSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "whsec_" + hex.EncodeToString(b[:])
}

// HashSecret is the hex sha256 kept for comparison.
func HashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// Sign returns the hex HMAC-SHA256 of timestamp + "." + body (spec 7.2).
func Sign(secret string, ts time.Time, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strconv.FormatInt(ts.Unix(), 10)))
	m.Write([]byte("."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// SignatureHeader joins one "v1=<sig>" per valid secret, so a receiver can
// accept either during a rotation overlap.
func SignatureHeader(secrets []string, ts time.Time, body []byte) string {
	parts := make([]string, len(secrets))
	for i, s := range secrets {
		parts[i] = "v1=" + Sign(s, ts, body)
	}
	return strings.Join(parts, ",")
}

// Verify is what a receiver runs: it checks the signature against any of the
// provided secrets and rejects timestamps outside the window (defeats replay).
func Verify(secrets []string, tsHeader, sigHeader string, body []byte, now time.Time, window time.Duration) bool {
	unix, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return false
	}
	ts := time.Unix(unix, 0)
	if d := now.Sub(ts); d > window || d < -window {
		return false
	}
	for _, s := range secrets {
		want := "v1=" + Sign(s, ts, body)
		for _, got := range strings.Split(sigHeader, ",") {
			if hmac.Equal([]byte(got), []byte(want)) {
				return true
			}
		}
	}
	return false
}
