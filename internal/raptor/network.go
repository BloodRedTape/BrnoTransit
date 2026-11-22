package raptor

import (
	"math"
	"strconv"
	"strings"

	"brnotransport/internal/gtfs"
)

const (
	walkRadiusMeters = 400.0
	walkSpeed        = 1.3
	walkDetour       = 1.25
	minWalkSeconds   = 60
)

type Footpath struct {
	To     int32
	Dur    int32
	Meters int32
}

type pattern struct {
	stops  []int32
	pickup []bool
	drop   []bool
	route  int32
	trips  []int32
}

type Network struct {
	Feed      *gtfs.Feed
	Footpaths [][]Footpath
	UsedStops []bool
	patterns  []pattern
	tripConns map[int32][]int32
}

func NewNetwork(f *gtfs.Feed) *Network {
	n := &Network{Feed: f, UsedStops: make([]bool, len(f.Stops)), tripConns: map[int32][]int32{}}
	n.buildPatterns()
	n.buildFootpaths()
	for i, t := range f.TripTransfers {
		n.tripConns[t.FromTrip] = append(n.tripConns[t.FromTrip], int32(i))
	}
	return n
}

func (n *Network) walkBetween(from, to int32) (dur, meters int32) {
	for _, fp := range n.Footpaths[from] {
		if fp.To == to {
			return fp.Dur, fp.Meters
		}
	}
	if from == to {
		return 0, 0
	}
	d := Distance(n.Feed.Stops[from], n.Feed.Stops[to]) * walkDetour
	return max(int32(math.Ceil(d/walkSpeed)), minWalkSeconds), int32(math.Round(d))
}

func (n *Network) buildPatterns() {
	index := map[string]int{}
	var sb strings.Builder
	for ti, t := range n.Feed.Trips {
		if len(t.Times) < 2 {
			continue
		}
		sb.Reset()
		sb.WriteString(strconv.Itoa(int(t.Route)))
		for _, st := range t.Times {
			sb.WriteByte('|')
			sb.WriteString(strconv.Itoa(int(st.Stop)))
			if !st.Pickup {
				sb.WriteByte('p')
			}
			if !st.Drop {
				sb.WriteByte('d')
			}
		}
		key := sb.String()
		pi, ok := index[key]
		if !ok {
			p := pattern{route: t.Route}
			for _, st := range t.Times {
				p.stops = append(p.stops, st.Stop)
				p.pickup = append(p.pickup, st.Pickup)
				p.drop = append(p.drop, st.Drop)
				n.UsedStops[st.Stop] = true
			}
			pi = len(n.patterns)
			index[key] = pi
			n.patterns = append(n.patterns, p)
		}
		n.patterns[pi].trips = append(n.patterns[pi].trips, int32(ti))
	}
}

func (n *Network) buildFootpaths() {
	stops := n.Feed.Stops
	n.Footpaths = make([][]Footpath, len(stops))

	const latCell, lonCell = 0.004, 0.006
	type cell struct{ a, b int }
	grid := map[cell][]int32{}
	cellOf := func(s gtfs.Stop) cell {
		return cell{int(math.Floor(s.Lat / latCell)), int(math.Floor(s.Lon / lonCell))}
	}
	for i, s := range stops {
		if n.UsedStops[i] {
			grid[cellOf(s)] = append(grid[cellOf(s)], int32(i))
		}
	}

	explicit := map[[2]int32]int32{}
	for _, t := range n.Feed.Transfers {
		if n.UsedStops[t.From] && n.UsedStops[t.To] {
			explicit[[2]int32{t.From, t.To}] = t.MinTime
		}
	}

	seen := map[[2]int32]bool{}
	add := func(from, to int32, minTime int32) {
		key := [2]int32{from, to}
		if seen[key] {
			return
		}
		seen[key] = true
		d := Distance(stops[from], stops[to]) * walkDetour
		dur := int32(math.Ceil(d / walkSpeed))
		if minTime > 0 {
			dur = minTime
		}
		if dur < minWalkSeconds {
			dur = minWalkSeconds
		}
		n.Footpaths[from] = append(n.Footpaths[from], Footpath{To: to, Dur: dur, Meters: int32(math.Round(d))})
	}

	for key, mt := range explicit {
		add(key[0], key[1], mt)
	}
	for i, s := range stops {
		if !n.UsedStops[i] {
			continue
		}
		c := cellOf(s)
		for da := -1; da <= 1; da++ {
			for db := -1; db <= 1; db++ {
				for _, j := range grid[cell{c.a + da, c.b + db}] {
					if int(j) == i || Distance(s, stops[j]) > walkRadiusMeters {
						continue
					}
					add(int32(i), j, explicit[[2]int32{int32(i), j}])
				}
			}
		}
	}
}

func Distance(a, b gtfs.Stop) float64 {
	const r = 6371000.0
	la1, la2 := a.Lat*math.Pi/180, b.Lat*math.Pi/180
	dla := la2 - la1
	dlo := (b.Lon - a.Lon) * math.Pi / 180
	h := math.Sin(dla/2)*math.Sin(dla/2) + math.Cos(la1)*math.Cos(la2)*math.Sin(dlo/2)*math.Sin(dlo/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(h)))
}
