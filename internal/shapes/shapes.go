package shapes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"brnotransport/internal/filecache"
)

const (
	DefaultURL = "https://services6.arcgis.com/fUWVlHWZNxUvTUh8/arcgis/rest/services/transit_routes/FeatureServer/0/query?where=1%3D1&outFields=naz_linky,typ&outSR=4326&f=geojson"

	snapMaxDist     = 80.0
	detourFactor    = 2.5
	detourAllowance = 300.0
	minFeatures     = 10
	checkInterval   = time.Hour
)

type polyline struct {
	pts [][2]float64
	cum []float64
}

type index map[string][]polyline

type Store struct {
	file *filecache.File
	idx  atomic.Pointer[index]
}

func NewStore(url, path string, ttl time.Duration) *Store {
	return &Store{file: &filecache.File{URL: url, Path: path, TTL: ttl}}
}

func (s *Store) Init(ctx context.Context) error {
	if !s.file.Stale() {
		if idx, err := load(s.file.Path); err == nil {
			s.install(idx)
			return nil
		}
	}
	if err := s.refresh(ctx); err != nil {
		if _, exists := s.file.Age(); !exists {
			return err
		}
		log.Printf("shapes: download failed (%v), using stale cache", err)
		idx, err := load(s.file.Path)
		if err != nil {
			return err
		}
		s.install(idx)
	}
	return nil
}

func (s *Store) Run(ctx context.Context) {
	t := time.NewTicker(checkInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.file.Stale() {
				if err := s.refresh(ctx); err != nil {
					log.Printf("shapes: refresh failed: %v", err)
				}
			}
		}
	}
}

func (s *Store) refresh(ctx context.Context) error {
	var idx index
	err := s.file.Fetch(ctx, func(path string) error {
		var err error
		idx, err = load(path)
		return err
	})
	if err != nil {
		return err
	}
	s.install(idx)
	return nil
}

func (s *Store) install(idx index) {
	n := 0
	for _, pls := range idx {
		n += len(pls)
	}
	s.idx.Store(&idx)
	log.Printf("shapes: loaded %d lines, %d polylines", len(idx), n)
}

type featureCollection struct {
	Features []struct {
		Properties struct {
			Name string `json:"naz_linky"`
		} `json:"properties"`
		Geometry struct {
			Type        string          `json:"type"`
			Coordinates json.RawMessage `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}

func load(path string) (index, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var fc featureCollection
	if err := json.Unmarshal(b, &fc); err != nil {
		return nil, err
	}
	if len(fc.Features) < minFeatures {
		return nil, fmt.Errorf("shapes: only %d features", len(fc.Features))
	}
	idx := index{}
	for _, f := range fc.Features {
		var parts [][][2]float64
		switch f.Geometry.Type {
		case "LineString":
			var ls [][2]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &ls); err != nil {
				continue
			}
			parts = [][][2]float64{ls}
		case "MultiLineString":
			if err := json.Unmarshal(f.Geometry.Coordinates, &parts); err != nil {
				continue
			}
		default:
			continue
		}
		for _, p := range parts {
			if len(p) < 2 {
				continue
			}
			pl := polyline{pts: make([][2]float64, len(p)), cum: make([]float64, len(p))}
			for i, c := range p {
				pl.pts[i] = [2]float64{c[1], c[0]}
				if i > 0 {
					pl.cum[i] = pl.cum[i-1] + dist(pl.pts[i-1], pl.pts[i])
				}
			}
			idx[f.Properties.Name] = append(idx[f.Properties.Name], pl)
		}
	}
	if len(idx) == 0 {
		return nil, errors.New("shapes: no usable geometry")
	}
	return idx, nil
}

func key(mode, line string) string {
	digits := line != "" && strings.Trim(line, "0123456789") == ""
	switch mode {
	case "tram":
		if n, err := strconv.Atoi(line); err == nil {
			return fmt.Sprintf("T%02d", n)
		}
	case "trolleybus":
		if digits {
			return "TB" + line
		}
	case "bus":
		if digits {
			return "B" + line
		}
		if len(line) > 1 && line[0] == 'N' && strings.Trim(line[1:], "0123456789") == "" {
			return "B" + line[1:]
		}
	case "train":
		return "V_" + line
	}
	return ""
}

func (s *Store) Shape(mode, line string, stops [][2]float64) [][2]float64 {
	idx := s.idx.Load()
	if idx == nil || len(stops) < 2 {
		return nil
	}
	cands := (*idx)[key(mode, line)]
	if len(cands) == 0 {
		cands = (*idx)[line]
	}
	if len(cands) == 0 {
		return nil
	}
	out := [][2]float64{stops[0]}
	snapped := false
	for i := 1; i < len(stops); i++ {
		seg := bestSlice(cands, stops[i-1], stops[i])
		if seg == nil {
			out = append(out, stops[i])
			continue
		}
		snapped = true
		out = append(out, seg...)
		out = append(out, stops[i])
	}
	if !snapped {
		return nil
	}
	return out
}

func bestSlice(cands []polyline, a, b [2]float64) [][2]float64 {
	straight := dist(a, b)
	var best [][2]float64
	bestScore := math.Inf(1)
	for k := range cands {
		pl := &cands[k]
		for _, reversed := range []bool{false, true} {
			from, to := a, b
			if reversed {
				from, to = b, a
			}
			m1, d1 := project(pl, from, 0)
			if d1 > snapMaxDist {
				continue
			}
			m2, d2 := project(pl, to, m1)
			if d2 > snapMaxDist || m2 <= m1 || m2-m1 > straight*detourFactor+detourAllowance {
				continue
			}
			if score := d1 + d2 + 0.1*(m2-m1); score < bestScore {
				bestScore, best = score, slice(pl, m1, m2)
				if reversed {
					slices.Reverse(best)
				}
			}
		}
	}
	return best
}

func project(pl *polyline, p [2]float64, from float64) (measure, distance float64) {
	kx := 111320 * math.Cos(p[0]*math.Pi/180)
	const ky = 110540.0
	distance = math.Inf(1)
	for i := 1; i < len(pl.pts); i++ {
		if pl.cum[i] < from {
			continue
		}
		segLen := pl.cum[i] - pl.cum[i-1]
		ax, ay := (pl.pts[i-1][1]-p[1])*kx, (pl.pts[i-1][0]-p[0])*ky
		bx, by := (pl.pts[i][1]-p[1])*kx, (pl.pts[i][0]-p[0])*ky
		dx, dy := bx-ax, by-ay
		l2 := dx*dx + dy*dy
		t := 0.0
		if l2 > 0 {
			t = math.Max(0, math.Min(1, -(ax*dx+ay*dy)/l2))
		}
		if segLen > 0 && pl.cum[i-1] < from {
			t = math.Max(t, (from-pl.cum[i-1])/segLen)
		}
		if d := math.Hypot(ax+t*dx, ay+t*dy); d < distance {
			distance, measure = d, pl.cum[i-1]+t*segLen
		}
	}
	return measure, distance
}

func pointAt(pl *polyline, m float64) [2]float64 {
	for i := 1; i < len(pl.pts); i++ {
		if pl.cum[i] >= m {
			segLen := pl.cum[i] - pl.cum[i-1]
			t := 0.0
			if segLen > 0 {
				t = (m - pl.cum[i-1]) / segLen
			}
			return [2]float64{
				pl.pts[i-1][0] + t*(pl.pts[i][0]-pl.pts[i-1][0]),
				pl.pts[i-1][1] + t*(pl.pts[i][1]-pl.pts[i-1][1]),
			}
		}
	}
	return pl.pts[len(pl.pts)-1]
}

func slice(pl *polyline, m1, m2 float64) [][2]float64 {
	out := [][2]float64{pointAt(pl, m1)}
	for i, c := range pl.cum {
		if c > m1 && c < m2 {
			out = append(out, pl.pts[i])
		}
	}
	return append(out, pointAt(pl, m2))
}

func dist(a, b [2]float64) float64 {
	kx := 111320 * math.Cos((a[0]+b[0])/2*math.Pi/180)
	return math.Hypot((b[1]-a[1])*kx, (b[0]-a[0])*110540)
}
