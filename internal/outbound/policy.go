package outbound

import (
	"sort"
	"time"
)

// Candidate is a position that could satisfy an allocation.
type Candidate struct {
	Location  string
	Lot       string
	LPN       string
	Available int64
	// ExpiresAt is nil for non-perishable stock.
	ExpiresAt *time.Time
	// PickFace is true when the location is in a pick-face zone.
	PickFace bool
}

// ApplyPolicy sorts candidates by the documented allocation policy (spec 6.2):
//  1. earliest expiry first; lots with no expiry sort after every dated lot
//  2. pick-face zone before bulk
//  3. lowest location code
//
// It is a pure function so the policy can be tested as a table.
func ApplyPolicy(cs []Candidate) []Candidate {
	out := append([]Candidate(nil), cs...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch {
		case a.ExpiresAt != nil && b.ExpiresAt == nil:
			return true
		case a.ExpiresAt == nil && b.ExpiresAt != nil:
			return false
		case a.ExpiresAt != nil && !a.ExpiresAt.Equal(*b.ExpiresAt):
			return a.ExpiresAt.Before(*b.ExpiresAt)
		}
		if a.PickFace != b.PickFace {
			return a.PickFace
		}
		if a.Location != b.Location {
			return a.Location < b.Location
		}
		if a.Lot != b.Lot {
			return a.Lot < b.Lot
		}
		return a.LPN < b.LPN
	})
	return out
}
