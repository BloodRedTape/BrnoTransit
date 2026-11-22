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
	via       int32
}

type connRecord struct {
	from     label
	fromStop int32
	arr      int32
	board    boarding
	conn     Connection
}

type Leg struct {
	Walk       bool
	From, To   int32
	Dep, Arr   int32
	Meters     int32
	Route      int32
	Trip       int32
	BoardPos   int32
	AlightPos  int32
	Guaranteed bool
	MaxWait    int32
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
	bestWalk  [][]int32
	transWalk [][]int32
	bestDep   [][]int32
	transDep  [][]int32
	tauStar   []int32
	isTarget  []bool
	targetArr int32
	slack     int32
	conns     []connRecord
	pending   []int32
	rounds    int
	excluded  map[int32]bool
	origin    []Footpath
}

type Options struct {
	MaxRides      int
	TransferSlack int32
	ArriveBy      int32
	ExcludedLines map[int32]bool
	Origin        []Footpath
}

func (tt *Timetable) OriginStop() int32 { return int32(len(tt.Net.Feed.Stops)) }

func TripKey(route, trip int32) int64 { return int64(route)<<32 | int64(trip) }

func (tt *Timetable) Search(sources, targets []int32, depart int32, maxRides int, transferSlack int32) []Journey {
	return tt.SearchWith(sources, targets, depart, Options{MaxRides: maxRides, TransferSlack: transferSlack})
}

func (tt *Timetable) SearchWith(sources, targets []int32, depart int32, o Options) []Journey {
	n := len(tt.Net.Feed.Stops)
	if len(o.Origin) > 0 {
		sources = []int32{tt.OriginStop()}
		n++
	}
	rounds := o.MaxRides + 1
	transferSlack := o.TransferSlack
	targetArr := int32(Infinity)
	if o.ArriveBy > 0 && o.ArriveBy < Infinity {
		targetArr = o.ArriveBy + 1
	}
	s := &search{
		tt:        tt,
		best:      make([][]int32, rounds),
		bestLab:   make([][]label, rounds),
		transit:   make([][]int32, rounds),
		transLab:  make([][]label, rounds),
		bestWalk:  make([][]int32, rounds),
		transWalk: make([][]int32, rounds),
		bestDep:   make([][]int32, rounds),
		transDep:  make([][]int32, rounds),
		tauStar:   fill(n),
		isTarget:  make([]bool, n),
		targetArr: targetArr,
		slack:     transferSlack,
		rounds:    rounds,
		excluded:  o.ExcludedLines,
		origin:    o.Origin,
	}
	for k := 0; k < rounds; k++ {
		s.best[k] = fill(n)
		s.bestLab[k] = make([]label, n)
		s.transit[k] = fill(n)
		s.transLab[k] = make([]label, n)
		s.bestWalk[k] = make([]int32, n)
		s.transWalk[k] = make([]int32, n)
		s.bestDep[k] = make([]int32, n)
		s.transDep[k] = make([]int32, n)
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
	if len(o.Origin) > 0 {
		s.relaxFootpaths(0, tt.OriginStop(), mark)
	}

	var results []Journey
	results = append(results, s.collect(0, Infinity)...)
	prevBest := s.targetArr

	queue := make(map[int32]int32)
	guaranteed := make(map[int32][]int32)
	enqueue := func(route, pos int32) {
		if p, ok := queue[route]; !ok || pos < p {
			queue[route] = pos
		}
	}
	for k := 1; k < rounds && (len(marked) > 0 || len(s.pending) > 0); k++ {
		copy(s.best[k], s.best[k-1])
		copy(s.bestLab[k], s.bestLab[k-1])
		copy(s.bestWalk[k], s.bestWalk[k-1])
		copy(s.bestDep[k], s.bestDep[k-1])

		clear(queue)
		clear(guaranteed)
		for _, st := range marked {
			isMarked[st] = false
			if st == tt.OriginStop() {
				continue
			}
			for _, sr := range tt.StopRoutes[st] {
				enqueue(sr.Route, sr.Pos)
			}
		}
		marked = marked[:0]
		for _, ci := range s.pending {
			c := s.conns[ci].conn
			enqueue(c.ToRoute, c.ToPos)
			guaranteed[c.ToRoute] = append(guaranteed[c.ToRoute], ci)
		}
		s.pending = s.pending[:0]

		for ri, start := range queue {
			s.scanRoute(k, ri, start, guaranteed[ri], mark)
		}

		transitMarked := append([]int32(nil), marked...)
		for _, st := range transitMarked {
			s.relaxFootpaths(k, st, mark)
		}

		if js := s.collect(k, prevBest); len(js) > 0 {
			results = append(results, js...)
			prevBest = js[0].Arr
		}
	}
	return results
}

func (s *search) scanRoute(k int, ri, start int32, guaranteed []int32, mark func(int32)) {
	r := &s.tt.Routes[ri]
	if s.excluded[r.GTFSRoute] {
		return
	}
	trip := -1
	var boardPos, boardVia int32
	var cur boarding
	for pos := int(start); pos < len(r.Stops); pos++ {
		stop := r.Stops[pos]
		if trip >= 0 && r.Drop[pos] {
			a := r.ArrAt(trip, pos)
			lab := label{kind: kindTransit, round: int32(k), route: ri, trip: int32(trip), boardPos: boardPos, alightPos: int32(pos), via: boardVia}
			if a < s.tauStar[stop] && s.reachesTarget(a, stop) {
				s.transit[k][stop], s.transLab[k][stop], s.transWalk[k][stop], s.transDep[k][stop] = a, lab, cur.walk, cur.dep
				s.best[k][stop], s.bestLab[k][stop], s.bestWalk[k][stop], s.bestDep[k][stop] = a, lab, cur.walk, cur.dep
				s.tauStar[stop] = a
				if s.isTarget[stop] {
					s.targetArr = a
				}
				mark(stop)
			}
			if r.HasConn != nil && r.HasConn[pos] && k+1 < s.rounds && a < s.targetArr {
				s.recordConnections(lab, stop, a, cur, r.Conns[int32(r.at(trip, pos))])
			}
		}
		for _, ci := range guaranteed {
			rec := &s.conns[ci]
			if int(rec.conn.ToPos) != pos {
				continue
			}
			t := int(rec.conn.ToTrip)
			dur, _ := s.tt.Net.walkBetween(rec.fromStop, stop)
			cand := boarding{walk: rec.board.walk + dur, dep: rec.board.dep, prev: rideKey(rec.from)}
			if trip < 0 || t < trip || (t == trip && cand.better(cur)) {
				trip, boardPos, cur, boardVia = t, int32(pos), cand, ci+1
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
		t := earliestTrip(r, pos, prev)
		if t < 0 {
			continue
		}
		cand := s.boardingAt(k-1, stop, r.DepAt(t, pos))
		if trip < 0 || t < trip || (t == trip && cand.better(cur)) {
			trip, boardPos, cur, boardVia = t, int32(pos), cand, 0
		}
	}
}

type boarding struct {
	walk int32
	dep  int32
	prev int64
}

func (b boarding) better(cur boarding) bool {
	if b.prev == cur.prev {
		return b.walk < cur.walk
	}
	return b.dep < cur.dep
}

func rideKey(l label) int64 {
	if l.kind != kindTransit {
		return -1
	}
	return TripKey(l.route, l.trip)
}

func (s *search) boardingAt(k int, stop, dep int32) boarding {
	lab := s.bestLab[k][stop]
	if lab.round == 0 {
		return boarding{walk: s.bestWalk[k][stop], dep: dep - s.bestWalk[k][stop], prev: -1}
	}
	if lab.kind == kindWalk {
		lab = s.transLab[lab.round][lab.from]
	}
	return boarding{walk: s.bestWalk[k][stop], dep: s.bestDep[k][stop], prev: rideKey(lab)}
}

func (s *search) recordConnections(from label, stop, arr int32, board boarding, conns []Connection) {
	for _, c := range conns {
		if s.tt.Routes[c.ToRoute].DepAt(int(c.ToTrip), int(c.ToPos)) >= s.targetArr {
			continue
		}
		s.pending = append(s.pending, int32(len(s.conns)))
		s.conns = append(s.conns, connRecord{from: from, fromStop: stop, arr: arr, board: board, conn: c})
	}
}

func (s *search) relaxFootpaths(k int, from int32, mark func(int32)) {
	base := s.transit[k][from]
	paths := s.origin
	if from != s.tt.OriginStop() {
		paths = s.tt.Net.Footpaths[from]
	}
	for _, fp := range paths {
		a := base + fp.Dur
		if a >= s.tauStar[fp.To] || !s.reachesTarget(a, fp.To) {
			continue
		}
		s.best[k][fp.To] = a
		s.bestLab[k][fp.To] = label{kind: kindWalk, round: int32(k), from: from, dur: fp.Dur, meters: fp.Meters}
		s.bestWalk[k][fp.To] = s.transWalk[k][from] + fp.Dur
		s.bestDep[k][fp.To] = s.transDep[k][from]
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

func (s *search) reachesTarget(a, stop int32) bool {
	return a < s.targetArr || (a == s.targetArr && s.isTarget[stop])
}

func (s *search) collect(k int, prevBest int32) []Journey {
	bestArr := prevBest
	var stops []int32
	for stop, arr := range s.best[k] {
		if !s.isTarget[stop] || arr > bestArr || s.bestLab[k][stop].round != int32(k) {
			continue
		}
		if arr < bestArr {
			bestArr, stops = arr, stops[:0]
		}
		if arr < prevBest {
			stops = append(stops, int32(stop))
		}
	}
	var out []Journey
	for _, stop := range stops {
		out = append(out, Journey{Legs: s.reconstruct(k, stop), Arr: bestArr})
	}
	return out
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
			ride := Leg{
				From:      board,
				To:        stop,
				Dep:       r.DepAt(int(cur.trip), int(cur.boardPos)),
				Arr:       r.ArrAt(int(cur.trip), int(cur.alightPos)),
				Route:     cur.route,
				Trip:      cur.trip,
				BoardPos:  cur.boardPos,
				AlightPos: cur.alightPos,
			}
			if cur.via == 0 {
				legs = append(legs, ride)
				stop = board
				cur = s.bestLab[cur.round-1][stop]
				continue
			}
			rec := &s.conns[cur.via-1]
			ride.Guaranteed, ride.MaxWait = true, rec.conn.MaxWait
			legs = append(legs, ride)
			if rec.fromStop != board {
				dur, meters := s.tt.Net.walkBetween(rec.fromStop, board)
				legs = append(legs, Leg{Walk: true, From: rec.fromStop, To: board, Dep: rec.arr, Arr: min(rec.arr+dur, ride.Dep), Meters: meters})
			}
			stop = rec.fromStop
			cur = rec.from
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
