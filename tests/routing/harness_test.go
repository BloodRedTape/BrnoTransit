package routing

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata"

	"brnotransport/internal/gtfs"
	"brnotransport/internal/model"
	"brnotransport/internal/planner"
)

const (
	feedEnv       = "BRNO_GTFS"
	defaultFeed   = "testdata/gtfs-2026-11-20.zip"
	transferSlack = time.Minute
)

var (
	setupOnce sync.Once
	sharedP   *planner.GTFSPlanner
	sharedLoc *time.Location
	setupErr  error
)

func feedPath() string {
	if p := os.Getenv(feedEnv); p != "" {
		return p
	}
	return defaultFeed
}

func gtfsPlanner(t *testing.T) (*planner.GTFSPlanner, *time.Location) {
	t.Helper()
	path := feedPath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("GTFS feed not found at %s: %v", path, err)
	}
	setupOnce.Do(func() {
		sharedLoc, setupErr = time.LoadLocation("Europe/Prague")
		if setupErr != nil {
			return
		}
		abs, _ := filepath.Abs(path)
		p := planner.NewGTFSPlanner(&gtfs.Cache{Path: abs, TTL: 100 * 365 * 24 * time.Hour}, sharedLoc)
		if setupErr = p.Init(context.Background()); setupErr == nil {
			sharedP = p
		}
	})
	if setupErr != nil {
		t.Fatalf("loading GTFS planner: %v", setupErr)
	}
	return sharedP, sharedLoc
}

type result struct {
	t        *testing.T
	from, to string
	depart   time.Time
	journeys []model.Journey
}

func plan(t *testing.T, from, to, when string, opts ...planner.Options) *result {
	t.Helper()
	var o planner.Options
	if len(opts) > 0 {
		o = opts[0]
	}
	r := planRaw(t, from, to, when, o)
	if len(r.journeys) == 0 {
		t.Fatalf("no journeys found (does the feed at %s cover %s?)", feedPath(), when)
	}
	r.checkInvariants()
	return r
}

func planFrom(t *testing.T, lat, lon float64, to, when string) *result {
	t.Helper()
	return plan(t, "", to, when, planner.Options{Origin: &model.Point{Lat: lat, Lon: lon}})
}

func planRaw(t *testing.T, from, to, when string, o planner.Options) *result {
	t.Helper()
	p, loc := gtfsPlanner(t)
	depart, err := time.ParseInLocation("2006-01-02 15:04", when, loc)
	if err != nil {
		t.Fatalf("bad time %q: %v", when, err)
	}
	if o.Origin != nil {
		from = planner.OriginName
	}
	js, err := p.Plan(context.Background(), from, to, depart, o)
	if err != nil {
		t.Fatalf("Plan(%q → %q at %s): %v", from, to, when, err)
	}
	r := &result{t: t, from: from, to: to, depart: depart, journeys: js}
	t.Logf("Plan %s → %s at %s:\n%s", from, to, when, r)
	return r
}

func (r *result) String() string {
	var sb strings.Builder
	for i, j := range r.journeys {
		fmt.Fprintf(&sb, "  #%d %s → %s\n", i+1, hm(j.Departure()), hm(j.Arrival()))
		for _, l := range j.Legs {
			sb.WriteString("      " + describeLeg(l) + "\n")
		}
	}
	return sb.String()
}

func describeLeg(l model.Leg) string {
	if l.IsWalk() {
		return fmt.Sprintf("walk %s → %s  %s-%s (%d m)", l.From.Name, l.To.Name, hm(l.Depart), hm(l.Arrive), l.DistanceMeters)
	}
	s := fmt.Sprintf("%s %s: %s %s → %s %s  [%s]", l.Mode, l.Line.Name, hm(l.Depart), l.From.Name, hm(l.Arrive), l.To.Name, l.TripID)
	if l.Guaranteed {
		s += fmt.Sprintf("  (guaranteed, waits up to %v)", l.MaxWait)
	}
	return s
}

func hm(t time.Time) string { return t.Format("15:04") }

func (r *result) checkInvariants() {
	t := r.t
	t.Helper()
	trips := map[string]int{}
	for i, j := range r.journeys {
		if len(j.Legs) == 0 {
			t.Errorf("journey #%d has no legs", i+1)
			continue
		}
		if j.Departure().Before(r.depart.Truncate(time.Minute)) {
			t.Errorf("journey #%d departs %s, before the requested %s", i+1, hm(j.Departure()), hm(r.depart))
		}
		for k, l := range j.Legs {
			if l.Arrive.Before(l.Depart) {
				t.Errorf("journey #%d leg %d arrives before it departs: %s", i+1, k+1, describeLeg(l))
			}
			if k == 0 {
				continue
			}
			prev := j.Legs[k-1]
			if prev.To.ID != l.From.ID {
				t.Errorf("journey #%d legs %d and %d are not connected: %s / %s", i+1, k, k+1, describeLeg(prev), describeLeg(l))
			}
			if l.Depart.Before(prev.Arrive) {
				t.Errorf("journey #%d leg %d departs before leg %d arrives", i+1, k+1, k)
			}
			if prev.IsWalk() && l.IsWalk() {
				t.Errorf("journey #%d has two consecutive walks (legs %d, %d)", i+1, k, k+1)
			}
			if !l.IsWalk() && !l.Guaranteed {
				if ride, ok := lastRideBefore(j, k); ok && l.Depart.Before(arrivalAt(j, ride, k).Add(transferSlack)) {
					t.Errorf("journey #%d: transfer to leg %d is shorter than %v slack", i+1, k+1, transferSlack)
				}
			}
		}
		key := tripSet(j)
		if prev, dup := trips[key]; dup && key != "" {
			t.Errorf("journeys #%d and #%d use the same trips (%s)", prev+1, i+1, key)
		}
		trips[key] = i
	}
}

func lastRideBefore(j model.Journey, k int) (int, bool) {
	if k >= 1 && !j.Legs[k-1].IsWalk() {
		return k - 1, true
	}
	if k >= 2 && j.Legs[k-1].IsWalk() && !j.Legs[k-2].IsWalk() {
		return k - 2, true
	}
	return 0, false
}

func arrivalAt(j model.Journey, ride, k int) time.Time {
	t := j.Legs[ride].Arrive
	for i := ride + 1; i < k; i++ {
		t = t.Add(j.Legs[i].Duration())
	}
	return t
}

func tripSet(j model.Journey) string {
	var ids []string
	for _, l := range j.Legs {
		if !l.IsWalk() {
			ids = append(ids, l.TripID)
		}
	}
	return strings.Join(ids, ";")
}

func (r *result) find(pred func(model.Journey) bool) (model.Journey, bool) {
	for _, j := range r.journeys {
		if pred(j) {
			return j, true
		}
	}
	return model.Journey{}, false
}

func (r *result) expectNone(what string, pred func(model.Journey) bool) {
	r.t.Helper()
	for i, j := range r.journeys {
		if pred(j) {
			r.t.Errorf("journey #%d should not exist: %s", i+1, what)
		}
	}
}

func (r *result) expectSome(what string, pred func(model.Journey) bool) {
	r.t.Helper()
	if _, ok := r.find(pred); !ok {
		r.t.Errorf("expected a journey that %s", what)
	}
}

func walks(from, to string) func(model.Journey) bool {
	return func(j model.Journey) bool {
		for _, l := range j.Legs {
			if l.IsWalk() && l.From.Name == from && l.To.Name == to {
				return true
			}
		}
		return false
	}
}

type ride struct{ line, from, to string }

func rides(want ...ride) func(model.Journey) bool {
	return func(j model.Journey) bool {
		var got []ride
		for _, l := range j.Legs {
			if !l.IsWalk() {
				got = append(got, ride{l.Line.Name, l.From.Name, l.To.Name})
			}
		}
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
}

func boards(line, stop, at string) func(model.Journey) bool {
	return func(j model.Journey) bool {
		for _, l := range j.Legs {
			if !l.IsWalk() && l.Line.Name == line && l.From.Name == stop && hm(l.Depart) == at {
				return true
			}
		}
		return false
	}
}

func lastLine(line string) func(model.Journey) bool {
	return func(j model.Journey) bool {
		last := j.Legs[len(j.Legs)-1]
		return !last.IsWalk() && last.Line.Name == line
	}
}

func noWalksBetweenStops(j model.Journey) bool {
	for _, l := range j.Legs {
		if l.IsWalk() && l.From.Name != l.To.Name {
			return false
		}
	}
	return true
}

func and(ps ...func(model.Journey) bool) func(model.Journey) bool {
	return func(j model.Journey) bool {
		for _, p := range ps {
			if !p(j) {
				return false
			}
		}
		return true
	}
}
