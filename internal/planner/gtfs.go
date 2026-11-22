package planner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"brnotransport/internal/gtfs"
	"brnotransport/internal/model"
	"brnotransport/internal/raptor"
)

const (
	maxRides        = 6
	wantedJourneys  = 6
	maxExtra        = 5
	maxIterations   = 20
	maxAlternatives = 10
	maxAltSearches  = 30
	altTolerance    = 5 * time.Minute
	searchWindow    = 3 * time.Hour
	refreshInterval = 10 * time.Minute
	transferSlack   = 1 * time.Minute
)

type GTFSPlanner struct {
	cache *gtfs.Cache
	loc   *time.Location
	data  atomic.Pointer[dataset]
}

type station struct {
	stop     model.Stop
	children []int32
}

type dataset struct {
	net      *raptor.Network
	loc      *time.Location
	stations []station
	stops    []model.Stop
	byName   map[string]int
	byFold   map[string]int
	byNumber map[int]string
	tripByID map[string]int32

	mu   sync.Mutex
	days map[int]*raptor.Timetable
}

func NewGTFSPlanner(cache *gtfs.Cache, loc *time.Location) *GTFSPlanner {
	return &GTFSPlanner{cache: cache, loc: loc}
}

func (p *GTFSPlanner) Init(ctx context.Context) error {
	var feed *gtfs.Feed
	var err error
	if !p.cache.Stale() {
		feed, err = p.cache.LoadCached()
		if err != nil {
			log.Printf("gtfs: cached feed unreadable (%v), downloading", err)
		}
	}
	if feed == nil {
		feed, err = p.cache.Fetch(ctx)
		if err != nil {
			if _, exists := p.cache.Age(); !exists {
				return fmt.Errorf("gtfs: download failed and no cache: %w", err)
			}
			log.Printf("gtfs: download failed (%v), using stale cache", err)
			if feed, err = p.cache.LoadCached(); err != nil {
				return err
			}
		}
	}
	p.install(feed)
	return nil
}

func (p *GTFSPlanner) Run(ctx context.Context) {
	t := time.NewTicker(refreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !p.cache.Stale() {
				continue
			}
			feed, err := p.cache.Fetch(ctx)
			if err != nil {
				log.Printf("gtfs: refresh failed: %v", err)
				continue
			}
			p.install(feed)
		}
	}
}

func (p *GTFSPlanner) install(feed *gtfs.Feed) {
	start := time.Now()
	d := newDataset(feed, p.loc)
	d.timetable(time.Now().In(p.loc))
	p.data.Store(d)
	log.Printf("gtfs: loaded %d stations, %d trips in %v", len(d.stations), len(feed.Trips), time.Since(start).Round(time.Millisecond))
}

func (p *GTFSPlanner) Stops() []model.Stop { return p.data.Load().stops }

type TripSchedule struct {
	Line       string
	Mode       string
	Stations   []int
	Points     [][2]float64
	Arr, Dep   []time.Time
	BoardIndex int
}

func (p *GTFSPlanner) TripSchedule(tripID, boardStopID string, boardDep time.Time) (TripSchedule, bool) {
	d := p.data.Load()
	feed := d.net.Feed
	ti, ok := d.tripByID[tripID]
	if !ok {
		return TripSchedule{}, false
	}
	board, ok := feed.StopIndex[boardStopID]
	if !ok {
		return TripSchedule{}, false
	}
	trip := &feed.Trips[ti]
	boardPos := -1
	for i, st := range trip.Times {
		if st.Stop == board {
			boardPos = i
			break
		}
	}
	if boardPos < 0 {
		return TripSchedule{}, false
	}
	base := boardDep.Add(-time.Duration(trip.Times[boardPos].Dep) * time.Second)
	route := feed.Routes[trip.Route]
	s := TripSchedule{Line: route.ShortName, Mode: string(modeOf(route.Type)), BoardIndex: boardPos}
	for _, st := range trip.Times {
		stop := feed.Stops[st.Stop]
		n, _ := stopNumber(stop.ID)
		s.Stations = append(s.Stations, n)
		s.Points = append(s.Points, [2]float64{stop.Lat, stop.Lon})
		s.Arr = append(s.Arr, base.Add(time.Duration(st.Arr)*time.Second))
		s.Dep = append(s.Dep, base.Add(time.Duration(st.Dep)*time.Second))
	}
	return s, true
}

func (p *GTFSPlanner) StopNameByNumber(n int) (string, bool) {
	name, ok := p.data.Load().byNumber[n]
	return name, ok
}

func stopNumber(id string) (int, bool) {
	if len(id) < 2 || id[0] != 'U' {
		return 0, false
	}
	end := 1
	for end < len(id) && id[end] >= '0' && id[end] <= '9' {
		end++
	}
	if end == 1 {
		return 0, false
	}
	n, err := strconv.Atoi(id[1:end])
	return n, err == nil
}

func (p *GTFSPlanner) Lookup(name string) (model.Stop, bool) {
	d := p.data.Load()
	i, ok := d.lookup(name)
	if !ok {
		return model.Stop{}, false
	}
	return d.stations[i].stop, true
}

func (p *GTFSPlanner) Plan(ctx context.Context, from, to string, depart time.Time, opts Options) ([]model.Journey, error) {
	d := p.data.Load()
	ti, ok := d.lookup(to)
	if !ok {
		return nil, ErrUnknownStop
	}
	targets := d.stations[ti].children
	base := raptor.Options{MaxRides: maxRides, TransferSlack: int32(transferSlack / time.Second)}
	var sources []int32
	if opts.Origin != nil {
		base.Origin = d.net.FootpathsFrom(opts.Origin.Lat, opts.Origin.Lon)
	} else {
		fi, ok := d.lookup(from)
		if !ok {
			return nil, ErrUnknownStop
		}
		if fi == ti {
			return nil, errors.New("origin equals destination")
		}
		sources = d.stations[fi].children
	}

	depart = depart.In(d.loc).Truncate(time.Minute)
	tt := d.timetable(depart)
	t0 := int32(depart.Sub(tt.Base) / time.Second)
	limit := t0 + int32(searchWindow/time.Second)

	bySet := map[string]int{}
	var found []raptor.Journey
	t := t0
	cutoff := int32(raptor.Infinity)
	for iter := 0; iter < maxIterations && len(found) < wantedJourneys+maxExtra && t <= limit; iter++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		js := tt.SearchWith(sources, targets, t, base)
		if len(js) == 0 {
			break
		}
		next := int32(raptor.Infinity)
		added := false
		for _, j := range js {
			j = alignInitialWalk(j)
			if dep := j.Legs[0].Dep; dep < next {
				next = dep
			}
			if j.Legs[0].Dep > limit || j.Arr > cutoff {
				continue
			}
			key := tripSetKey(j)
			if i, ok := bySet[key]; !ok {
				if len(found) >= wantedJourneys+maxExtra {
					continue
				}
				bySet[key] = len(found)
				found = append(found, j)
				added = true
			} else if betterOnSameTrips(j, found[i]) {
				found[i] = j
			}
		}
		if len(found) >= wantedJourneys {
			if cutoff != raptor.Infinity && !added {
				break
			}
			cutoff = latestArrival(found)
		}
		if next < t {
			next = t
		}
		if next >= limit {
			break
		}
		t = next + 60
	}

	if opts.Alternatives {
		found = addAlternatives(ctx, tt, sources, targets, found, bySet, limit, base)
	}

	out := make([]model.Journey, 0, len(found))
	for _, j := range found {
		out = append(out, d.toModel(tt, j, opts.Origin))
	}
	return out, nil
}

func addAlternatives(ctx context.Context, tt *raptor.Timetable, sources, targets []int32, found []raptor.Journey, bySet map[string]int, limit int32, base raptor.Options) []raptor.Journey {
	if len(found) == 0 {
		return found
	}
	cutoff := latestArrival(found)
	type node struct {
		j, root  raptor.Journey
		excluded map[int32]bool
	}
	queue := make([]node, 0, len(found))
	for _, j := range found {
		queue = append(queue, node{j: j, root: j})
	}
	tried := map[string]bool{}
	searches, added := 0, 0
	for len(queue) > 0 && searches < maxAltSearches && added < maxAlternatives && ctx.Err() == nil {
		n := queue[0]
		queue = queue[1:]
		var lines []int32
		for _, l := range n.j.Legs {
			if !l.Walk {
				lines = append(lines, tt.Routes[l.Route].GTFSRoute)
			}
		}
		for _, line := range lines[min(1, len(lines)):] {
			excluded := make(map[int32]bool, len(n.excluded)+1)
			for k := range n.excluded {
				excluded[k] = true
			}
			excluded[line] = true
			dep := n.j.Legs[0].Dep
			key := exclusionKey(dep, excluded)
			if tried[key] {
				continue
			}
			tried[key] = true
			if searches >= maxAltSearches || added >= maxAlternatives {
				break
			}
			searches++
			arriveBy := min(cutoff, n.root.Arr+int32(altTolerance/time.Second))
			o := base
			o.MaxRides, o.ArriveBy, o.ExcludedLines = n.root.Rides(), arriveBy, excluded
			js := tt.SearchWith(sources, targets, dep, o)
			for _, j := range js {
				j = alignInitialWalk(j)
				if j.Legs[0].Dep > limit || j.Arr > arriveBy {
					continue
				}
				key := tripSetKey(j)
				if i, ok := bySet[key]; ok {
					if betterOnSameTrips(j, found[i]) {
						found[i] = j
					}
					continue
				}
				if added >= maxAlternatives {
					break
				}
				bySet[key] = len(found)
				found = append(found, j)
				added++
				queue = append(queue, node{j: j, root: n.root, excluded: excluded})
			}
		}
	}
	return found
}

func exclusionKey(dep int32, excluded map[int32]bool) string {
	keys := make([]string, 0, len(excluded))
	for k := range excluded {
		keys = append(keys, strconv.Itoa(int(k)))
	}
	sort.Strings(keys)
	return strconv.Itoa(int(dep)) + ":" + strings.Join(keys, ",")
}

func alignInitialWalk(j raptor.Journey) raptor.Journey {
	if len(j.Legs) < 2 || !j.Legs[0].Walk {
		return j
	}
	legs := append([]raptor.Leg(nil), j.Legs...)
	dur := legs[0].Arr - legs[0].Dep
	legs[0].Arr = legs[1].Dep
	legs[0].Dep = legs[1].Dep - dur
	j.Legs = legs
	return j
}

func latestArrival(js []raptor.Journey) int32 {
	var latest int32
	for _, j := range js {
		latest = max(latest, j.Arr)
	}
	return latest
}

func walkSeconds(j raptor.Journey) int32 {
	var w int32
	for _, l := range j.Legs {
		if l.Walk {
			w += l.Arr - l.Dep
		}
	}
	return w
}

func betterOnSameTrips(a, b raptor.Journey) bool {
	if a.Arr != b.Arr {
		return a.Arr < b.Arr
	}
	if wa, wb := walkSeconds(a), walkSeconds(b); wa != wb {
		return wa < wb
	}
	if a.Legs[0].Dep != b.Legs[0].Dep {
		return a.Legs[0].Dep > b.Legs[0].Dep
	}
	return len(a.Legs) < len(b.Legs)
}

func tripSetKey(j raptor.Journey) string {
	var trips []string
	for _, l := range j.Legs {
		if !l.Walk {
			trips = append(trips, fmt.Sprintf("%d.%d", l.Route, l.Trip))
		}
	}
	sort.Strings(trips)
	return strings.Join(trips, ";")
}

func newDataset(feed *gtfs.Feed, loc *time.Location) *dataset {
	net := raptor.NewNetwork(feed)
	d := &dataset{net: net, loc: loc, byName: map[string]int{}, byFold: map[string]int{}, byNumber: map[int]string{}, tripByID: make(map[string]int32, len(feed.Trips)), days: map[int]*raptor.Timetable{}}
	for i, t := range feed.Trips {
		d.tripByID[t.ID] = int32(i)
	}
	for _, s := range feed.Stops {
		if n, ok := stopNumber(s.ID); ok {
			if _, seen := d.byNumber[n]; !seen || s.Station {
				d.byNumber[n] = s.Name
			}
		}
	}

	parentOf := func(i int) int {
		s := feed.Stops[i]
		if s.Parent != "" {
			if pi, ok := feed.StopIndex[s.Parent]; ok {
				return int(pi)
			}
		}
		return i
	}
	childIdx := map[int]int{}
	for i, s := range feed.Stops {
		if s.Station || !net.UsedStops[i] {
			continue
		}
		pi := parentOf(i)
		si, ok := childIdx[pi]
		if !ok {
			si = len(d.stations)
			childIdx[pi] = si
			ps := feed.Stops[pi]
			d.stations = append(d.stations, station{stop: model.Stop{ID: ps.ID, Name: ps.Name}})
		}
		d.stations[si].children = append(d.stations[si].children, int32(i))
	}

	count := map[string]int{}
	for _, st := range d.stations {
		count[st.stop.Name]++
	}
	for i := range d.stations {
		st := &d.stations[i]
		if count[st.stop.Name] > 1 {
			zone := feed.Stops[feed.StopIndex[st.stop.ID]].Zone
			st.stop.Name = fmt.Sprintf("%s (zone %s, %s)", st.stop.Name, zone, st.stop.ID)
		}
	}
	sort.Slice(d.stations, func(a, b int) bool { return d.stations[a].stop.Name < d.stations[b].stop.Name })

	foldCount := map[string]int{}
	for i, st := range d.stations {
		d.byName[normalize(st.stop.Name)] = i
		f := fold(st.stop.Name)
		foldCount[f]++
		d.byFold[f] = i
		d.stops = append(d.stops, st.stop)
	}
	for f, c := range foldCount {
		if c > 1 {
			delete(d.byFold, f)
		}
	}
	return d
}

func (d *dataset) lookup(name string) (int, bool) {
	if i, ok := d.byName[normalize(name)]; ok {
		return i, true
	}
	i, ok := d.byFold[fold(name)]
	return i, ok
}

func (d *dataset) timetable(t time.Time) *raptor.Timetable {
	y, m, day := t.Date()
	base := time.Date(y, m, day, 0, 0, 0, 0, d.loc)
	key := gtfs.DateKey(base)

	d.mu.Lock()
	defer d.mu.Unlock()
	if tt, ok := d.days[key]; ok {
		return tt
	}
	if len(d.days) >= 3 {
		clear(d.days)
	}
	tt := d.net.Timetable(base)
	d.days[key] = tt
	return tt
}

func (d *dataset) toModel(tt *raptor.Timetable, j raptor.Journey, origin *model.Point) model.Journey {
	feed := d.net.Feed
	at := func(sec int32) time.Time { return tt.Base.Add(time.Duration(sec) * time.Second) }
	stop := func(i int32) gtfs.Stop {
		if i == tt.OriginStop() {
			return gtfs.Stop{Name: OriginName, Lat: origin.Lat, Lon: origin.Lon}
		}
		return feed.Stops[i]
	}
	var out model.Journey
	for _, l := range j.Legs {
		from, to := stop(l.From), stop(l.To)
		leg := model.Leg{
			From:         model.Stop{ID: from.ID, Name: from.Name},
			To:           model.Stop{ID: to.ID, Name: to.Name},
			FromPlatform: platform(from),
			ToPlatform:   platform(to),
			Depart:       at(l.Dep),
			Arrive:       at(l.Arr),
		}
		pt := func(s gtfs.Stop) model.Point { return model.Point{Lat: s.Lat, Lon: s.Lon} }
		if l.Walk {
			leg.Mode = model.Walk
			leg.DistanceMeters = int(l.Meters)
			leg.Path = []model.Point{pt(from), pt(to)}
		} else {
			r := &tt.Routes[l.Route]
			gr := feed.Routes[r.GTFSRoute]
			leg.Mode = modeOf(gr.Type)
			leg.Line = model.Line{Name: gr.ShortName, Color: hexColor(gr.Color, "#2f86d0"), TextColor: hexColor(gr.TextColor, "#ffffff")}
			leg.Headsign = feed.Trips[r.Trips[l.Trip]].Headsign
			leg.TripID = feed.Trips[r.Trips[l.Trip]].ID
			leg.IntermediateStops = int(l.AlightPos - l.BoardPos - 1)
			leg.Guaranteed = l.Guaranteed
			leg.MaxWait = time.Duration(l.MaxWait) * time.Second
			meters := 0.0
			leg.Path = append(leg.Path, pt(from))
			for p := l.BoardPos; p < l.AlightPos; p++ {
				meters += raptor.Distance(feed.Stops[r.Stops[p]], feed.Stops[r.Stops[p+1]])
				leg.Path = append(leg.Path, pt(feed.Stops[r.Stops[p+1]]))
			}
			leg.DistanceMeters = int(meters)
		}
		out.Legs = append(out.Legs, leg)
	}
	return out
}

func platform(s gtfs.Stop) string {
	if s.Platform != "" {
		return s.Platform
	}
	if i := strings.LastIndexByte(s.ID, 'Z'); i >= 0 && i+1 < len(s.ID) {
		return s.ID[i+1:]
	}
	return ""
}

func modeOf(routeType int) model.Mode {
	switch {
	case routeType == 0 || routeType == 900:
		return model.Tram
	case routeType == 11 || routeType == 800:
		return model.Trolleybus
	case routeType == 2 || (routeType >= 100 && routeType < 200):
		return model.Train
	default:
		return model.Bus
	}
}

func hexColor(c, fallback string) string {
	if len(c) != 6 {
		return fallback
	}
	for _, r := range c {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return fallback
		}
	}
	return "#" + c
}

var foldReplacer = strings.NewReplacer(
	"á", "a", "ä", "a", "č", "c", "ď", "d", "é", "e", "ě", "e", "í", "i", "ľ", "l", "ĺ", "l",
	"ň", "n", "ó", "o", "ô", "o", "ö", "o", "ř", "r", "ŕ", "r", "š", "s", "ť", "t",
	"ú", "u", "ů", "u", "ü", "u", "ý", "y", "ž", "z",
)

func fold(name string) string {
	return foldReplacer.Replace(normalize(name))
}
