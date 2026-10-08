// Package settlement is cash-on-delivery settlement (spec 9): courier delivery
// outcomes, remittance files, matching, breaks and the cases that resolve them.
// The matching rules are pure functions so each break type in spec 9.2 is a row
// in a table test.
package settlement

import (
	"fmt"
	"strings"
)

// Break kinds (spec 9.2).
const (
	ShortRemitted       = "short_remitted"
	Unremitted          = "unremitted"
	RemittedForRejected = "remitted_for_rejected"
	UnknownShipment     = "unknown_shipment"
	DuplicateRemittance = "duplicate_remittance"
)

// Outcomes a courier reports (spec 9.1).
const (
	DeliveredCollected = "delivered_collected"
	Rejected           = "rejected"
	Lost               = "lost"
)

// Terms are what was agreed with a courier.
type Terms struct {
	FeeBps         int   // COD fee as basis points of the amount collected
	FeeFlat        int64 // plus a flat amount, minor units
	ShippingCost   int64 // what sending one parcel costs
	SettlementDays int   // days after delivery by which the courier should have paid
}

// DefaultTerms apply to a courier with nothing agreed on file: no fee, seven days.
var DefaultTerms = Terms{SettlementDays: 7}

// ExpectedFee is the fee the courier may deduct from a collected amount,
// rounding half up.
func (t Terms) ExpectedFee(collected int64) int64 {
	return (collected*int64(t.FeeBps)+5000)/10000 + t.FeeFlat
}

// ExpectedNet is what the courier should pay over for a COD amount.
func (t Terms) ExpectedNet(cod int64) int64 { return cod - t.ExpectedFee(cod) }

// LineFacts is everything the matcher knows when it looks at one remittance line.
type LineFacts struct {
	Tracking      string
	ShipmentFound bool
	// COD is the shipment's cash-on-delivery amount; nil for a prepaid shipment.
	COD *int64
	// HasRejected: the courier reported this shipment rejected.
	HasRejected bool
	// AlreadyRemitted: an earlier line (this file or another) already paid this shipment.
	AlreadyRemitted bool
	Collected, Fee, Net int64
	Terms               Terms
}

// Verdict is a break found on a line.
type Verdict struct {
	Kind             string
	Expected, Actual int64
	Detail           string
}

// ClassifyLine decides whether a remittance line matches. ok is false for a
// line that matches cleanly. The order of the checks matters: a line for a
// shipment we never sent is unknown before anything else, and a repeated line
// is a duplicate whatever its amount.
func ClassifyLine(f LineFacts) (v Verdict, ok bool) {
	switch {
	case !f.ShipmentFound:
		return Verdict{UnknownShipment, 0, f.Net, fmt.Sprintf("tracking %s was never dispatched by this warehouse", f.Tracking)}, true
	case f.AlreadyRemitted:
		return Verdict{DuplicateRemittance, 0, f.Net, "this shipment was already remitted"}, true
	case f.HasRejected:
		return Verdict{RemittedForRejected, 0, f.Net, "the courier reported this parcel rejected and paid for it anyway"}, true
	case f.COD == nil:
		return Verdict{UnknownShipment, 0, f.Net, "the shipment is prepaid; no cash on delivery was due"}, true
	}
	want := f.Terms.ExpectedNet(*f.COD)
	if f.Net < want {
		return Verdict{ShortRemitted, want, f.Net, fmt.Sprintf("collected %d, fee %d, net %d; expected net %d (COD %d less agreed fee %d)",
			f.Collected, f.Fee, f.Net, want, *f.COD, f.Terms.ExpectedFee(*f.COD))}, true
	}
	return Verdict{}, false
}

// ParseMoney reads a decimal amount with at most two decimals into minor units:
// "1250", "1250.5", "1250.50". Signs, separators and extra decimals are refused.
func ParseMoney(s string) (int64, error) {
	s = strings.TrimSpace(s)
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" || (hasDot && (frac == "" || len(frac) > 2)) {
		return 0, fmt.Errorf("%q is not an amount with at most two decimals", s)
	}
	var n int64
	for _, r := range whole + frac {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%q is not an amount", s)
		}
	}
	for _, r := range whole {
		n = n*10 + int64(r-'0')
		if n > 1<<50 {
			return 0, fmt.Errorf("%q is too large", s)
		}
	}
	n *= 100
	switch len(frac) {
	case 1:
		n += int64(frac[0]-'0') * 10
	case 2:
		n += int64(frac[0]-'0')*10 + int64(frac[1]-'0')
	}
	return n, nil
}

// FormatMoney is the inverse of ParseMoney.
func FormatMoney(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s%d.%02d", sign, minor/100, minor%100)
}
