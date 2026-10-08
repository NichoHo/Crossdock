// Package counting is cycle counting and variance classification (spec 8.1,
// 8.2, 8.4). The rules here are pure functions so each classification in the
// spec is a table row in a test.
package counting

// Class is a variance classification.
type Class string

const (
	Timing     Class = "timing"
	Miscount   Class = "miscount"
	Damaged    Class = "damaged"
	Shrinkage  Class = "shrinkage"
	Found      Class = "found"
	Unresolved Class = "unresolved"
)

// Valid reports whether c is one of the six classes.
func (c Class) Valid() bool {
	switch c {
	case Timing, Miscount, Damaged, Shrinkage, Found, Unresolved:
		return true
	}
	return false
}

// KeyState is what the log says about one (sku, lot, lpn) at the counted
// location, in shelf units (units physically on the shelf: available, reserved,
// quarantined and damaged, but not picked units that are in transit).
type KeyState struct {
	// Base is the shelf quantity of everything committed when the count started.
	Base int64
	// End is the shelf quantity of everything committed when it finished.
	End int64
	// Deltas are the shelf effects of the movements that committed in between.
	Deltas []int64
}

// maxSubset caps the subset-sum search. Beyond it only the sequential states
// (start, after the first movement, after the second, ...) are tried.
const maxSubset = 64

// Explains reports whether counted equals the shelf quantity at some moment
// while the count ran: the start quantity plus the effect of any subset of the
// movements that committed meanwhile. The operator walks the shelf while pickers
// keep working, so each in-flight movement either happened before the operator
// looked at that item or after. When this is true the difference is timing, not
// loss.
func (k KeyState) Explains(counted int64) bool {
	if counted == k.Base || counted == k.End {
		return true
	}
	if len(k.Deltas) == 0 {
		return false
	}
	if len(k.Deltas) > maxSubset {
		sum := k.Base
		for _, d := range k.Deltas {
			sum += d
			if sum == counted {
				return true
			}
		}
		return false
	}
	reach := map[int64]struct{}{k.Base: {}}
	for _, d := range k.Deltas {
		next := make(map[int64]struct{}, len(reach)*2)
		for v := range reach {
			next[v] = struct{}{}
			next[v+d] = struct{}{}
		}
		reach = next
	}
	_, ok := reach[counted]
	return ok
}

// FirstCount classifies a first count of one key. A difference that no in-flight
// movement explains cannot be judged from one count: it needs a recount.
func FirstCount(counted int64, k KeyState) (c Class, needsRecount bool, differs bool) {
	if counted == k.End {
		return "", false, false
	}
	if k.Explains(counted) {
		return Timing, false, true
	}
	return Unresolved, true, true
}

// Recount classifies a key after its recount (spec 8.2). first is the first
// count, again the recount, and late is true when the recount finished more than
// an hour after the first count.
func Recount(first, again int64, k KeyState, late bool) Class {
	switch {
	case late:
		return Unresolved
	case again == k.End:
		return Miscount // the recount returns the system quantity: the first count was wrong
	case again == first && again < k.End:
		return Shrinkage
	case again == first && again > k.End:
		return Found
	case k.Explains(again):
		return Timing
	}
	return Unresolved
}

// Ageing buckets (spec 8.4).
type Ageing struct {
	Under7   int `json:"under_7_days"`
	D7to30   int `json:"d7_to_30"`
	D30to90  int `json:"d30_to_90"`
	Over90   int `json:"over_90"`
}

// Total is the number of open items.
func (a Ageing) Total() int { return a.Under7 + a.D7to30 + a.D30to90 + a.Over90 }
