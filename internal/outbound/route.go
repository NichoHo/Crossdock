package outbound

import (
	"fmt"
	"math"
	"strconv"
)

// Floor graph (spec 6.3). A location code AISLE-BAY-LEVEL-POSITION such as
// A07-12-C-2 maps to (aisle 7, bay 12). Aisles run parallel; the cross aisle is
// at bay 0, which is also where the dock is. Distance between two points in
// different aisles is: walk to the cross aisle, across, and up the other aisle.
const (
	aisleSpacingM = 3.0 // metres between aisle centre lines
	baySpacingM   = 1.2 // metres per bay
)

type point struct{ aisle, bay int }

func parse(code string) (point, error) {
	if len(code) < 6 || code[3] != '-' {
		return point{}, fmt.Errorf("outbound: bad location code %q", code)
	}
	a, err1 := strconv.Atoi(code[1:3])
	b, err2 := strconv.Atoi(code[4:6])
	if err1 != nil || err2 != nil {
		return point{}, fmt.Errorf("outbound: bad location code %q", code)
	}
	return point{a, b}, nil
}

func dist(a, b point) float64 {
	if a.aisle == b.aisle {
		return math.Abs(float64(a.bay-b.bay)) * baySpacingM
	}
	return float64(a.bay)*baySpacingM + float64(b.bay)*baySpacingM +
		math.Abs(float64(a.aisle-b.aisle))*aisleSpacingM
}

// Route orders stops (location codes) into a walking route starting and ending
// at the dock (aisle 0, bay 0). It uses nearest-neighbour then a 2-opt pass.
// It returns the visiting order as indices into stops, and the route length in
// metres. The result is deterministic for a given input.
func Route(stops []string) (order []int, metres float64, err error) {
	pts := make([]point, len(stops))
	for i, s := range stops {
		if pts[i], err = parse(s); err != nil {
			return nil, 0, err
		}
	}
	n := len(pts)
	if n == 0 {
		return nil, 0, nil
	}
	dock := point{}

	// Nearest neighbour; ties break on the lower index for determinism.
	used := make([]bool, n)
	cur := dock
	for len(order) < n {
		best, bd := -1, math.MaxFloat64
		for i := 0; i < n; i++ {
			if used[i] {
				continue
			}
			if d := dist(cur, pts[i]); d < bd {
				best, bd = i, d
			}
		}
		used[best] = true
		order = append(order, best)
		cur = pts[best]
	}

	length := func(o []int) float64 {
		d, prev := 0.0, dock
		for _, i := range o {
			d += dist(prev, pts[i])
			prev = pts[i]
		}
		return d + dist(prev, dock)
	}

	// 2-opt: reverse a segment while it shortens the route.
	// ponytail: each pass is O(n^3), so very large waves keep the nearest-neighbour
	// route. Compute the delta of a swap instead of the whole length if waves of
	// more than 200 lines matter.
	for improved := n <= 200; improved; {
		improved = false
		for i := 0; i < n-1; i++ {
			for j := i + 1; j < n; j++ {
				cand := append([]int(nil), order...)
				for l, r := i, j; l < r; l, r = l+1, r-1 {
					cand[l], cand[r] = cand[r], cand[l]
				}
				if length(cand) < length(order)-1e-9 {
					order, improved = cand, true
				}
			}
		}
	}
	return order, length(order), nil
}
