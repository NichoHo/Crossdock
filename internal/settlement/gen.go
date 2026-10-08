package settlement

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"math/rand/v2"
)

// GenShipment is what the generator knows about a dispatched shipment.
type GenShipment struct {
	Tracking string
	COD      int64
	// Outcome is what the courier reported: delivered_collected, rejected, lost, or "".
	Outcome string
}

// GenConfig sets how many of each kind of break the generated file contains
// (spec 9.4). Counts are exact so a test can assert the matcher found them all.
type GenConfig struct {
	Seed  uint64
	Terms Terms
	// Short lines remit 5 to 30 percent less than the agreed net.
	Short int
	// Duplicate lines are remitted twice.
	Duplicate int
	// Unremitted shipments are delivered but left out of the file.
	Unremitted int
	// RemittedForRejected lines pay for a parcel the courier reported rejected.
	RemittedForRejected int
	// Unknown lines carry a tracking number nobody dispatched.
	Unknown int
}

// GenOutput is the CSV and the ground truth to check the matcher against.
type GenOutput struct {
	CSV []byte
	// Expected counts the breaks that should be found: the four that appear on
	// lines are found at ingest, unremitted once the settlement window passes.
	Expected map[string]int
	// ByTracking maps a tracking number to the break kind it should produce.
	ByTracking map[string]string
	// Clean is the number of lines that should match.
	Clean int
	// CleanNet is the sum of their net amounts.
	CleanNet int64
}

// GenerateRemittance writes a remittance CSV for shipments with a known set of
// breaks. It is deterministic for a seed.
func GenerateRemittance(ships []GenShipment, cfg GenConfig) (GenOutput, error) {
	var delivered, rejected []GenShipment
	for _, s := range ships {
		switch {
		case s.Outcome == DeliveredCollected && s.COD > 0:
			delivered = append(delivered, s)
		case s.Outcome == Rejected:
			rejected = append(rejected, s)
		}
	}
	if need := cfg.Short + cfg.Duplicate + cfg.Unremitted; need > len(delivered) {
		return GenOutput{}, fmt.Errorf("settlement: %d breaks need %d delivered COD shipments, have %d", need, need, len(delivered))
	}
	if cfg.RemittedForRejected > len(rejected) {
		return GenOutput{}, fmt.Errorf("settlement: %d remitted-for-rejected breaks need that many rejected shipments, have %d", cfg.RemittedForRejected, len(rejected))
	}
	rng := rand.New(rand.NewPCG(cfg.Seed, 0x9e3779b97f4a7c15))
	rng.Shuffle(len(delivered), func(i, j int) { delivered[i], delivered[j] = delivered[j], delivered[i] })
	rng.Shuffle(len(rejected), func(i, j int) { rejected[i], rejected[j] = rejected[j], rejected[i] })

	out := GenOutput{Expected: map[string]int{}, ByTracking: map[string]string{}}
	type line struct{ tracking string; collected, fee, net int64 }
	var lines []line
	clean := func(s GenShipment) line {
		fee := cfg.Terms.ExpectedFee(s.COD)
		return line{s.Tracking, s.COD, fee, s.COD - fee}
	}

	i := 0
	for ; i < cfg.Unremitted; i++ { // delivered, but no line in the file
		out.ByTracking[delivered[i].Tracking] = Unremitted
		out.Expected[Unremitted]++
	}
	for n := 0; n < cfg.Short; n, i = n+1, i+1 {
		s := delivered[i]
		want := cfg.Terms.ExpectedNet(s.COD)
		cut := max(1, want*int64(5+rng.IntN(26))/100)
		net := want - cut
		lines = append(lines, line{s.Tracking, s.COD, s.COD - net, net}) // the courier "deducts" more than agreed
		out.ByTracking[s.Tracking] = ShortRemitted
		out.Expected[ShortRemitted]++
	}
	for n := 0; n < cfg.Duplicate; n, i = n+1, i+1 {
		l := clean(delivered[i])
		lines = append(lines, l, l)
		out.ByTracking[delivered[i].Tracking] = DuplicateRemittance
		out.Expected[DuplicateRemittance]++
	}
	for ; i < len(delivered); i++ {
		l := clean(delivered[i])
		lines = append(lines, l)
		out.Clean++
		out.CleanNet += l.net
	}
	for n := 0; n < cfg.RemittedForRejected; n++ {
		s := rejected[n]
		cod := s.COD
		if cod == 0 {
			cod = 50000
		}
		fee := cfg.Terms.ExpectedFee(cod)
		lines = append(lines, line{s.Tracking, cod, fee, cod - fee})
		out.ByTracking[s.Tracking] = RemittedForRejected
		out.Expected[RemittedForRejected]++
	}
	for n := 0; n < cfg.Unknown; n++ {
		tr := fmt.Sprintf("UNK-%d-%04d", cfg.Seed, n)
		collected := int64(20000 + rng.IntN(80000))
		fee := cfg.Terms.ExpectedFee(collected)
		lines = append(lines, line{tr, collected, fee, collected - fee})
		out.ByTracking[tr] = UnknownShipment
		out.Expected[UnknownShipment]++
	}
	rng.Shuffle(len(lines), func(a, b int) { lines[a], lines[b] = lines[b], lines[a] })

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"courier_ref", "tracking", "collected", "fee", "net"})
	for n, l := range lines {
		_ = w.Write([]string{fmt.Sprintf("CR-%06d", n+1), l.tracking, FormatMoney(l.collected), FormatMoney(l.fee), FormatMoney(l.net)})
	}
	w.Flush()
	out.CSV = buf.Bytes()
	return out, w.Error()
}
