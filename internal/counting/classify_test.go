package counting

import "testing"

func TestExplains(t *testing.T) {
	cases := []struct {
		name    string
		k       KeyState
		counted int64
		want    bool
	}{
		{"counted the start quantity", KeyState{Base: 10, End: 7, Deltas: []int64{-3}}, 10, true},
		{"counted the end quantity", KeyState{Base: 10, End: 7, Deltas: []int64{-3}}, 7, true},
		{"counted between two picks", KeyState{Base: 10, End: 5, Deltas: []int64{-2, -3}}, 8, true},
		{"counted after only the later pick", KeyState{Base: 10, End: 5, Deltas: []int64{-2, -3}}, 7, true},
		{"a loss no movement explains", KeyState{Base: 10, End: 7, Deltas: []int64{-3}}, 5, false},
		{"no movements at all", KeyState{Base: 10, End: 10}, 8, false},
		{"receive and pick together", KeyState{Base: 10, End: 12, Deltas: []int64{5, -3}}, 15, true},
		{"too many movements fall back to sequential states", KeyState{Base: 100, End: 0, Deltas: seq(70, -1)}, 70, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.k.Explains(c.counted); got != c.want {
				t.Fatalf("Explains(%d) = %v, want %v", c.counted, got, c.want)
			}
		})
	}
}

func seq(n int, d int64) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = d
	}
	return out
}

func TestFirstCount(t *testing.T) {
	cases := []struct {
		name         string
		counted      int64
		k            KeyState
		wantClass    Class
		wantRecount  bool
		wantDiffers  bool
	}{
		{"agrees", 10, KeyState{Base: 10, End: 10}, "", false, false},
		{"timing from an in-flight pick", 10, KeyState{Base: 10, End: 8, Deltas: []int64{-2}}, Timing, false, true},
		{"unexplained loss needs a recount", 6, KeyState{Base: 10, End: 10}, Unresolved, true, true},
		{"unexplained surplus needs a recount", 12, KeyState{Base: 10, End: 10}, Unresolved, true, true},
		{"explained by one pick but counted lower", 5, KeyState{Base: 10, End: 8, Deltas: []int64{-2}}, Unresolved, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl, rc, d := FirstCount(c.counted, c.k)
			if cl != c.wantClass || rc != c.wantRecount || d != c.wantDiffers {
				t.Fatalf("got (%q, recount=%v, differs=%v), want (%q, %v, %v)", cl, rc, d, c.wantClass, c.wantRecount, c.wantDiffers)
			}
		})
	}
}

// The rows of the table in spec 8.2 that depend on a recount.
func TestRecount(t *testing.T) {
	steady := KeyState{Base: 10, End: 10}
	cases := []struct {
		name         string
		first, again int64
		k            KeyState
		late         bool
		want         Class
	}{
		{"recount returns the system quantity: miscount", 6, 10, steady, false, Miscount},
		{"recount agrees with a lower first count: shrinkage", 6, 6, steady, false, Shrinkage},
		{"recount agrees with a higher first count: found", 13, 13, steady, false, Found},
		{"recount disagrees with both: unresolved", 6, 8, steady, false, Unresolved},
		{"recount after the hour: unresolved even if it agrees", 6, 6, steady, true, Unresolved},
		{"recount after the hour that returns the system quantity: still unresolved", 6, 10, steady, true, Unresolved},
		{"recount explained by a pick during the recount: timing", 6, 10, KeyState{Base: 10, End: 8, Deltas: []int64{-2}}, false, Timing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Recount(c.first, c.again, c.k, c.late); got != c.want {
				t.Fatalf("Recount = %q, want %q", got, c.want)
			}
		})
	}
}
