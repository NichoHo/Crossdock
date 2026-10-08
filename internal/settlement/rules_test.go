package settlement

import "testing"

func cod(n int64) *int64 { return &n }

// Each break type in spec 9.2 is a row.
func TestClassifyLine(t *testing.T) {
	terms := Terms{FeeBps: 200, FeeFlat: 1000} // 2% + 10.00
	// COD 1000.00 -> fee 20.00 + 10.00 = 30.00 -> expected net 970.00
	good := LineFacts{Tracking: "T1", ShipmentFound: true, COD: cod(100000), Collected: 100000, Fee: 3000, Net: 97000, Terms: terms}

	cases := []struct {
		name     string
		mod      func(*LineFacts)
		wantKind string // "" = matches cleanly
		wantExp  int64
		wantAct  int64
	}{
		{"clean line matches", func(*LineFacts) {}, "", 0, 0},
		{"paid more than expected still matches", func(f *LineFacts) { f.Net = 98000 }, "", 0, 0},
		{"one satang short is short", func(f *LineFacts) { f.Net = 96999 }, ShortRemitted, 97000, 96999},
		{"fee deducted twice is short", func(f *LineFacts) { f.Fee, f.Net = 6000, 94000 }, ShortRemitted, 97000, 94000},
		{"customer paid less than the COD", func(f *LineFacts) { f.Collected, f.Fee, f.Net = 80000, 2600, 77400 }, ShortRemitted, 97000, 77400},
		{"unknown tracking number", func(f *LineFacts) { f.ShipmentFound = false }, UnknownShipment, 0, 97000},
		{"already remitted", func(f *LineFacts) { f.AlreadyRemitted = true }, DuplicateRemittance, 0, 97000},
		{"courier reported rejected but paid", func(f *LineFacts) { f.HasRejected = true }, RemittedForRejected, 0, 97000},
		{"duplicate beats rejected", func(f *LineFacts) { f.AlreadyRemitted, f.HasRejected = true, true }, DuplicateRemittance, 0, 97000},
		{"unknown beats everything", func(f *LineFacts) { f.ShipmentFound, f.AlreadyRemitted = false, true }, UnknownShipment, 0, 97000},
		{"prepaid shipment has nothing to remit", func(f *LineFacts) { f.COD = nil }, UnknownShipment, 0, 97000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := good
			c.mod(&f)
			v, broke := ClassifyLine(f)
			if (c.wantKind != "") != broke || v.Kind != c.wantKind {
				t.Fatalf("got %+v (break=%v), want kind %q", v, broke, c.wantKind)
			}
			if broke && (v.Expected != c.wantExp || v.Actual != c.wantAct) {
				t.Errorf("expected/actual = %d/%d, want %d/%d", v.Expected, v.Actual, c.wantExp, c.wantAct)
			}
		})
	}
}

func TestExpectedFeeRoundsHalfUp(t *testing.T) {
	cases := []struct {
		terms     Terms
		collected int64
		want      int64
	}{
		{Terms{FeeBps: 250}, 10000, 250},
		{Terms{FeeBps: 250}, 10002, 250},                // 250.05 rounds down
		{Terms{FeeBps: 250}, 10020, 251},                // exactly 250.5 rounds up
		{Terms{FeeBps: 150, FeeFlat: 500}, 12345, 685},  // 185.175 -> 185, plus 500
		{Terms{}, 99999, 0},
	}
	for _, c := range cases {
		if got := c.terms.ExpectedFee(c.collected); got != c.want {
			t.Errorf("ExpectedFee(%+v, %d) = %d, want %d", c.terms, c.collected, got, c.want)
		}
	}
}

func TestParseMoney(t *testing.T) {
	ok := map[string]int64{"0": 0, "1250": 125000, "1250.5": 125050, "1250.50": 125050, "0.07": 7, " 12.30 ": 1230}
	for in, want := range ok {
		if got, err := ParseMoney(in); err != nil || got != want {
			t.Errorf("ParseMoney(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "-5", "+5", "1,250.00", "12.345", "12.", ".5", "abc", "1e3", "99999999999999999999"} {
		if _, err := ParseMoney(in); err == nil {
			t.Errorf("ParseMoney(%q) should fail", in)
		}
	}
	if FormatMoney(125050) != "1250.50" || FormatMoney(7) != "0.07" || FormatMoney(-250) != "-2.50" {
		t.Error("FormatMoney round trip")
	}
}
