// Package ids generates short, prefixed, random identifiers.
package ids

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns prefix + "-" + 12 hex chars (48 bits of randomness).
func New(prefix string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand failing is unrecoverable
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
