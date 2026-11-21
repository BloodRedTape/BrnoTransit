package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"brnotransport/internal/live"
	"brnotransport/internal/planner"
)

const (
	maxTrackedLegs   = 10
	earlyTolerance   = 2 * time.Minute
	lateTolerance    = 3 * time.Minute
	layoverAllowance = 15 * time.Minute
	maxLiveLines     = 20
	streamBatchEvery = 300 * time.Millisecond
	streamPingEvery  = 20 * time.Second
)

type StopNamer interface {
	StopNameByNumber(n int) (string, bool)
}

type vehicleJSON struct {
	ID       int     `json:"id"`
	Line     string  `json:"line"`
	Lat      float64 `json:"lat,omitempty"`
	Lon      float64 `json:"lon,omitempty"`
	Bearing  int     `json:"bearing,omitempty"`
	Delay    int     `json:"delay,omitempty"`
	Final    string  `json:"final,omitempty"`
	LowFloor bool    `json:"lowFloor,omitempty"`
	Updated  int64   `json:"updated,omitempty"`
	Gone     bool    `json:"gone,omitempty"`
}

type snapshotJSON struct {
	Connected bool          `json:"connected"`
	Vehicles  []vehicleJSON `json:"vehicles"`
}

func (s *Server) SetLive(t *live.Tracker, names StopNamer, trips TripResolver) {
	s.live, s.stopNames, s.trips = t, names, trips
}

func parseLines(r *http.Request) map[string]bool {
	lines := map[string]bool{}
	for _, l := range strings.Split(r.URL.Query().Get("lines"), ",") {
		if l = strings.TrimSpace(l); l != "" && len(lines) < maxLiveLines {
			lines[l] = true
		}
	}
	return lines
}

func (s *Server) vehicleJSON(v live.Vehicle) vehicleJSON {
	if v.Gone {
		return vehicleJSON{ID: v.ID, Line: v.Line, Gone: true}
	}
	vj := vehicleJSON{ID: v.ID, Line: v.Line, Lat: v.Lat, Lon: v.Lon, Bearing: v.Bearing, Delay: v.Delay, LowFloor: v.LowFloor, Updated: v.Updated.Unix()}
	if s.stopNames != nil {
		vj.Final, _ = s.stopNames.StopNameByNumber(v.FinalStop)
	}
	return vj
}

func (s *Server) snapshot(lines map[string]bool) snapshotJSON {
	out := snapshotJSON{Connected: s.live.Connected(), Vehicles: []vehicleJSON{}}
	for _, v := range s.live.Vehicles(lines) {
		out.Vehicles = append(out.Vehicles, s.vehicleJSON(v))
	}
	return out
}

func (s *Server) liveRequest(w http.ResponseWriter, r *http.Request) (map[string]bool, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if s.live == nil {
		http.Error(w, "live data disabled", http.StatusServiceUnavailable)
		return nil, false
	}
	lines := parseLines(r)
	if len(lines) == 0 {
		http.Error(w, "no lines requested", http.StatusBadRequest)
		return nil, false
	}
	return lines, true
}

func (s *Server) handleVehicles(w http.ResponseWriter, r *http.Request) {
	lines, ok := s.liveRequest(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.snapshot(lines))
}

type TripResolver interface {
	TripSchedule(tripID, boardStopID string, boardDep time.Time) (planner.TripSchedule, bool)
}

type legState struct {
	Line      string       `json:"line"`
	Dep       string       `json:"dep"`
	Status    string       `json:"status"`
	Vehicle   *vehicleJSON `json:"vehicle,omitempty"`
	StartTime string       `json:"startTime,omitempty"`
	StartStop string       `json:"startStop,omitempty"`
}

type tripState struct {
	Connected bool       `json:"connected"`
	Legs      []legState `json:"legs"`
}

type trackedLeg struct {
	sched    planner.TripSchedule
	resolved bool
	dep      string
}

func matchTrip(v live.Vehicle, s planner.TripSchedule) (time.Duration, bool) {
	if v.Line != s.Line || v.LastStop == 0 {
		return 0, false
	}
	delay := time.Duration(v.Delay) * time.Minute
	var best time.Duration
	found := false
	for i, st := range s.Stations {
		if st != v.LastStop {
			continue
		}
		diff := v.Updated.Sub(s.Dep[i].Add(delay))
		lo := -earlyTolerance
		if i == 0 {
			lo = -layoverAllowance
		}
		hi := lateTolerance
		if i+1 < len(s.Stations) {
			hi += s.Arr[i+1].Sub(s.Dep[i])
		}
		if diff < lo || diff > hi {
			continue
		}
		if diff < 0 {
			diff = -diff
		}
		if !found || diff < best {
			best, found = diff, true
		}
	}
	return best, found
}

func (s *Server) parseTrackedLegs(specs []string) []trackedLeg {
	legs := make([]trackedLeg, 0, len(specs))
	for _, spec := range specs {
		var tl trackedLeg
		parts := strings.Split(spec, "~")
		if len(parts) == 3 {
			if unix, err := strconv.ParseInt(parts[2], 10, 64); err == nil {
				dep := time.Unix(unix, 0)
				tl.dep = dep.In(s.loc).Format("15:04")
				tl.sched, tl.resolved = s.trips.TripSchedule(parts[0], parts[1], dep)
			}
		}
		legs = append(legs, tl)
	}
	return legs
}

func (s *Server) tripState(legs []trackedLeg, lines map[string]bool) tripState {
	vehicles := s.live.Vehicles(lines)
	now := time.Now()
	st := tripState{Connected: s.live.Connected(), Legs: make([]legState, len(legs))}
	for i, tl := range legs {
		ls := legState{Line: tl.sched.Line, Dep: tl.dep, Status: "unknown"}
		if tl.resolved {
			ls.Status = "waiting"
			if len(tl.sched.Dep) > 0 && now.Before(tl.sched.Dep[0]) {
				ls.Status = "not_started"
				ls.StartTime = tl.sched.Dep[0].In(s.loc).Format("15:04")
				if s.stopNames != nil {
					ls.StartStop, _ = s.stopNames.StopNameByNumber(tl.sched.Stations[0])
				}
			}
			var best time.Duration
			for _, v := range vehicles {
				score, ok := matchTrip(v, tl.sched)
				if ok && (ls.Vehicle == nil || score < best) {
					vj := s.vehicleJSON(v)
					ls.Vehicle, best, ls.Status = &vj, score, "live"
				}
			}
			if ls.Vehicle != nil {
				ls.StartTime, ls.StartStop = "", ""
			}
		}
		st.Legs[i] = ls
	}
	return st
}

func (s *Server) handleVehicleStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.live == nil || s.trips == nil {
		http.Error(w, "live data disabled", http.StatusServiceUnavailable)
		return
	}
	specs := r.URL.Query()["leg"]
	if len(specs) == 0 || len(specs) > maxTrackedLegs {
		http.Error(w, "bad legs", http.StatusBadRequest)
		return
	}
	legs := s.parseTrackedLegs(specs)
	lines := map[string]bool{}
	for _, tl := range legs {
		if tl.resolved {
			lines[tl.sched.Line] = true
		}
	}

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no")

	updates, unsubscribe := s.live.Subscribe(lines)
	defer unsubscribe()

	var last []byte
	sendState := func() error {
		b, err := json.Marshal(s.tripState(legs, lines))
		if err != nil || bytes.Equal(b, last) {
			return err
		}
		last = b
		if _, err := fmt.Fprintf(w, "event: state\ndata: %s\n\n", b); err != nil {
			return err
		}
		return rc.Flush()
	}

	if _, err := fmt.Fprint(w, "retry: 3000\n\n"); err != nil {
		return
	}
	if err := sendState(); err != nil {
		return
	}

	batch := time.NewTicker(streamBatchEvery)
	defer batch.Stop()
	ping := time.NewTicker(streamPingEvery)
	defer ping.Stop()
	dirty := false
	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates:
			dirty = true
		case <-batch.C:
			if !dirty {
				continue
			}
			dirty = false
			if err := sendState(); err != nil {
				return
			}
		case <-ping.C:
			if err := sendState(); err != nil {
				return
			}
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}
