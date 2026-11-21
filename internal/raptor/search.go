package raptor

import (
	"math"
	"sort"
)

const Infinity = math.MaxInt32

type labelKind uint8

const (
	kindNone labelKind = iota
	kindSource
	kindTransit
	kindWalk
)

type label struct {
	kind      labelKind
	round     int32
	route     int32
	trip      int32
	boardPos  int32
	alightPos int32
	from      int32
	dur       int32
	meters    int32
}

type Leg struct {
	Walk      bool
	From, To  int32
	Dep, Arr  int32
	Meters    int32
	Route     int32
	Trip      int32
	BoardPos  int32
	AlightPos int32
}

type Journey struct {
	Legs []Leg
	Arr  int32
}

func (j Journey) Rides() int {
	n := 0
	for _, l := range j.Legs {
		if !l.Walk {
			n++
		}
	}
	return n
}

type search struct {
	tt        *Timetable
	best      [][]int32
	bestLab   [][]label
	transit   [][]int32
	transLab  [][]label
	tauStar   []int32
	isTarget  []bool
	targetArr int32
	slack     int32
}

func (tt *Timetable) Search(sources, targets []int32, depart int32, maxRides int, transferSlack int32) []Journey {
	n := len(tt.Net.Feed.Stops)
	rounds := maxRides + 1
	s := &search{
		tt:        tt,
		best:      make([][]int32, rounds),
		bestLab:   make([][]label, rounds),
		transit:   make([][]int32, rounds),
		transLab:  make([][]label, rounds),
		tauStar:   fill(n),
		isTarget:  make([]bool, n),
		targetArr: Infinity,
		slack:     transferSlack,
	}
	for k := 0; k < rounds; k++ {
		s.best[k] = fill(n)
		s.bestLab[k] = make([]label, n)
		s.transit[k] = fill(n)
		s.transLab[k] = make([]label, n)
	}
	for _, t := range targets {
		s.isTarget[t] = true
	}

	var marked []int32
	isMarked := make([]bool, n)
	mark := func(stop int32) {
		if !isMarked[stop] {
			isMarked[stop] = true
			marked = append(marked, stop)
		}
	}

	src := label{kind: kindSource}
	for _, st := range sources {
		s.best[0][st], s.transit[0][st], s.tauStar[st] = depart, depart, depart
		s.bestLab[0][st], s.transLab[0][st] = src, src
		mark(st)
		if s.isTarget[st] {
			return nil
		}
	}
	for _, st := range append([]int32(nil), marked...) {
		s.relaxFootpaths(0, st, mark)
	}

	var results []Journey
	if j, ok := s.collect(0, Infinity); ok {
		results = append(results, j)
	}
	prevBest := s.targetArr

	queue := make(map[int32]int32)
	for k := 1; k < rounds && len(marked) > 0; k++ {
		copy(s.best[k], s.best[k-1])
		copy(s.bestLab[k], s.bestLab[k-1])

		clear(queue)
		for _, st := range marked {
			for _, sr := range tt.StopRoutes[st] {
				if p, ok := queue[sr.Route]; !ok || sr.Pos < p {
					queue[sr.Route] = sr.Pos
				}
			}
			isMarked[st] = false
		}
		marked = marked[:0]

		for ri, start := range queue {
			s.scanRoute(k, ri, start, mark)
		}

		transitMarked := append([]int32(nil), marked...)
		for _, st := range transitMarked {
			s.relaxFootpaths(k, st, mark)
		}

		if j, ok := s.collect(k, prevBest); ok {
			results = append(results, j)
			prevBest = j.Arr
		}
	}
	return results
}

func (s *search) scanRoute(k int, ri, start int32, mark func(int32)) {
	r := &s.tt.Routes[ri]
	trip := -1
	var boardPos int32
	for pos := int(start); pos < len(r.Stops); pos++ {
		stop := r.Stops[pos]
		if trip >= 0 && r.Drop[pos] {
			a := r.ArrAt(trip, pos)
			if a < s.tauStar[stop] && a < s.targetArr {
				lab := label{kind: kindTransit, round: int32(k), route: ri, trip: int32(trip), boardPos: boardPos, alightPos: int32(pos)}
				s.transit[k][stop], s.transLab[k][stop] = a, lab
				s.best[k][stop], s.bestLab[k][stop] = a, lab
				s.tauStar[stop] = a
				if s.isTarget[stop] {
					s.targetArr = a
				}
				mark(stop)
			}
		}
		prev := s.best[k-1][stop]
		if prev == Infinity || !r.Pickup[pos] {
			continue
		}
		if s.bestLab[k-1][stop].round > 0 {
			prev += s.slack
		}
		if trip >= 0 && prev > r.DepAt(trip, pos) {
			continue
		}
		if t := earliestTrip(r, pos, prev); t >= 0 && (trip < 0 || t < trip) {
			trip, boardPos = t, int32(pos)
		}
	}
}

func (s *search) relaxFootpaths(k int, from int32, mark func(int32)) {
	base := s.transit[k][from]
	for _, fp := range s.tt.Net.Footpaths[from] {
		a := base + fp.Dur
		if a >= s.tauStar[fp.To] || a >= s.targetArr {
			continue
		}
		s.best[k][fp.To] = a
		s.bestLab[k][fp.To] = label{kind: kindWalk, round: int32(k), from: from, dur: fp.Dur, meters: fp.Meters}
		s.tauStar[fp.To] = a
		if s.isTarget[fp.To] {
			s.targetArr = a
		}
		mark(fp.To)
	}
}

func earliestTrip(r *Route, pos int, t int32) int {
	n := len(r.Trips)
	i := sort.Search(n, func(i int) bool { return r.DepAt(i, pos) >= t })
	if i == n {
		return -1
	}
	return i
}

func (s *search) collect(k int, prevBest int32) (Journey, bool) {
	bestStop, bestArr := int32(-1), prevBest
	for stop, arr := range s.best[k] {
		if s.isTarget[stop] && arr < bestArr && s.bestLab[k][stop].round == int32(k) {
			bestStop, bestArr = int32(stop), arr
		}
	}
	if bestStop < 0 {
		return Journey{}, false
	}
	return Journey{Legs: s.reconstruct(k, bestStop), Arr: bestArr}, true
}

func (s *search) reconstruct(k int, stop int32) []Leg {
	var legs []Leg
	cur := s.bestLab[k][stop]
	for cur.kind != kindSource && cur.kind != kindNone {
		switch cur.kind {
		case kindWalk:
			dep := s.transit[cur.round][cur.from]
			legs = append(legs, Leg{Walk: true, From: cur.from, To: stop, Dep: dep, Arr: dep + cur.dur, Meters: cur.meters})
			stop = cur.from
			cur = s.transLab[cur.round][stop]
		case kindTransit:
			r := &s.tt.Routes[cur.route]
			board := r.Stops[cur.boardPos]
			legs = append(legs, Leg{
				From:      board,
				To:        stop,
				Dep:       r.DepAt(int(cur.trip), int(cur.boardPos)),
				Arr:       r.ArrAt(int(cur.trip), int(cur.alightPos)),
				Route:     cur.route,
				Trip:      cur.trip,
				BoardPos:  cur.boardPos,
				AlightPos: cur.alightPos,
			})
			stop = board
			cur = s.bestLab[cur.round-1][stop]
		}
	}
	for i, j := 0, len(legs)-1; i < j; i, j = i+1, j-1 {
		legs[i], legs[j] = legs[j], legs[i]
	}
	return legs
}

func fill(n int) []int32 {
	a := make([]int32, n)
	for i := range a {
		a[i] = Infinity
	}
	return a
}
