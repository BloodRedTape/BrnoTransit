package raptor

import (
	"sort"
	"time"
)

const (
	day            = 24 * 3600
	nextDayHorizon = 8 * 3600
)

type Route struct {
	GTFSRoute int32
	Stops     []int32
	Pickup    []bool
	Drop      []bool
	Trips     []int32
	Arr       []int32
	Dep       []int32
}

func (r *Route) at(trip, pos int) int { return trip*len(r.Stops) + pos }

func (r *Route) ArrAt(trip, pos int) int32 { return r.Arr[r.at(trip, pos)] }

func (r *Route) DepAt(trip, pos int) int32 { return r.Dep[r.at(trip, pos)] }

type StopRoute struct {
	Route int32
	Pos   int32
}

type Timetable struct {
	Net        *Network
	Base       time.Time
	Routes     []Route
	StopRoutes [][]StopRoute
}

type candidate struct {
	trip   int32
	offset int32
}

func (n *Network) Timetable(base time.Time) *Timetable {
	tt := &Timetable{Net: n, Base: base, StopRoutes: make([][]StopRoute, len(n.Feed.Stops))}
	prev, next := base.AddDate(0, 0, -1), base.AddDate(0, 0, 1)
	activeCache := map[int32][3]bool{}
	active := func(svc int32) [3]bool {
		if a, ok := activeCache[svc]; ok {
			return a
		}
		s := &n.Feed.Services[svc]
		a := [3]bool{s.ActiveOn(prev), s.ActiveOn(base), s.ActiveOn(next)}
		activeCache[svc] = a
		return a
	}

	for _, p := range n.patterns {
		var cands []candidate
		for _, ti := range p.trips {
			t := &n.Feed.Trips[ti]
			a := active(t.Service)
			if a[0] && t.Times[len(t.Times)-1].Arr >= day {
				cands = append(cands, candidate{ti, -day})
			}
			if a[1] {
				cands = append(cands, candidate{ti, 0})
			}
			if a[2] && t.Times[0].Dep < nextDayHorizon {
				cands = append(cands, candidate{ti, day})
			}
		}
		if len(cands) == 0 {
			continue
		}
		tt.addPattern(p, cands)
	}

	for ri, r := range tt.Routes {
		for pos, s := range r.Stops {
			tt.StopRoutes[s] = append(tt.StopRoutes[s], StopRoute{Route: int32(ri), Pos: int32(pos)})
		}
	}
	return tt
}

func (tt *Timetable) addPattern(p pattern, cands []candidate) {
	trips := tt.Net.Feed.Trips
	dep := func(c candidate, pos int) int32 { return trips[c.trip].Times[pos].Dep + c.offset }
	arr := func(c candidate, pos int) int32 { return trips[c.trip].Times[pos].Arr + c.offset }
	last := len(p.stops) - 1
	sort.Slice(cands, func(i, j int) bool {
		di, dj := dep(cands[i], 0), dep(cands[j], 0)
		if di != dj {
			return di < dj
		}
		return arr(cands[i], last) < arr(cands[j], last)
	})

	var groups [][]candidate
	for _, c := range cands {
		placed := false
		for g := range groups {
			prev := groups[g][len(groups[g])-1]
			ok := true
			for pos := range p.stops {
				if arr(prev, pos) > arr(c, pos) || dep(prev, pos) > dep(c, pos) {
					ok = false
					break
				}
			}
			if ok {
				groups[g] = append(groups[g], c)
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, []candidate{c})
		}
	}

	for _, g := range groups {
		r := Route{
			GTFSRoute: p.route,
			Stops:     p.stops,
			Pickup:    p.pickup,
			Drop:      p.drop,
			Trips:     make([]int32, len(g)),
			Arr:       make([]int32, 0, len(g)*len(p.stops)),
			Dep:       make([]int32, 0, len(g)*len(p.stops)),
		}
		for i, c := range g {
			r.Trips[i] = c.trip
			for pos := range p.stops {
				r.Arr = append(r.Arr, arr(c, pos))
				r.Dep = append(r.Dep, dep(c, pos))
			}
		}
		tt.Routes = append(tt.Routes, r)
	}
}
