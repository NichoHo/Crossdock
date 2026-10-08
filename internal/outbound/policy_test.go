package outbound

import (
	"reflect"
	"testing"
	"time"
)

func TestOrderPolicy(t *testing.T) {
	soon := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	later := soon.AddDate(0, 6, 0)
	cases := []struct {
		name string
		in   []Candidate
		want []string // location order
	}{
		{"earliest expiry first", []Candidate{{Location: "A01-01-A-1", ExpiresAt: &later}, {Location: "B01-01-A-1", ExpiresAt: &soon}}, []string{"B01-01-A-1", "A01-01-A-1"}},
		{"dated before undated", []Candidate{{Location: "A01-01-A-1"}, {Location: "B01-01-A-1", ExpiresAt: &later}}, []string{"B01-01-A-1", "A01-01-A-1"}},
		{"pick face before bulk", []Candidate{{Location: "A01-01-A-1"}, {Location: "C05-01-A-1", PickFace: true}}, []string{"C05-01-A-1", "A01-01-A-1"}},
		{"lowest code last tiebreak", []Candidate{{Location: "B01-01-A-1"}, {Location: "A01-01-A-1"}}, []string{"A01-01-A-1", "B01-01-A-1"}},
		{"expiry beats pick face", []Candidate{{Location: "A01-01-A-1", PickFace: true, ExpiresAt: &later}, {Location: "Z01-01-A-1", ExpiresAt: &soon}}, []string{"Z01-01-A-1", "A01-01-A-1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, x := range ApplyPolicy(c.in) {
				got = append(got, x.Location)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestRouteVisitsEveryStopAndNeverWorseThanInput(t *testing.T) {
	stops := []string{"C03-10-A-1", "A01-02-A-1", "B02-05-A-1", "A01-09-A-1", "C03-01-A-1"}
	order, metres, err := Route(stops)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for _, i := range order {
		seen[i] = true
	}
	if len(order) != len(stops) || len(seen) != len(stops) {
		t.Fatalf("route must visit each stop once: %v", order)
	}
	// Naive input-order length as an upper bound.
	naive, prev := 0.0, point{}
	for _, s := range stops {
		p, _ := parse(s)
		naive += dist(prev, p)
		prev = p
	}
	naive += dist(prev, point{})
	if metres > naive+1e-9 {
		t.Fatalf("route %.1fm is longer than input order %.1fm", metres, naive)
	}
	again, m2, _ := Route(stops)
	if !reflect.DeepEqual(order, again) || m2 != metres {
		t.Fatal("route is not deterministic")
	}
}

func TestRouteRejectsBadCode(t *testing.T) {
	if _, _, err := Route([]string{"nope"}); err == nil {
		t.Fatal("expected error")
	}
}
