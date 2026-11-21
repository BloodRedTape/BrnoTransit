package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"brnotransport/internal/live"
)

const (
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

func (s *Server) SetLive(t *live.Tracker, names StopNamer) {
	s.live, s.stopNames = t, names
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

func (s *Server) handleVehicleStream(w http.ResponseWriter, r *http.Request) {
	lines, ok := s.liveRequest(w, r)
	if !ok {
		return
	}
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no")

	updates, unsubscribe := s.live.Subscribe(lines)
	defer unsubscribe()

	send := func(event string, payload any) error {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return err
		}
		return rc.Flush()
	}

	if _, err := fmt.Fprint(w, "retry: 3000\n\n"); err != nil {
		return
	}
	if err := send("snapshot", s.snapshot(lines)); err != nil {
		return
	}

	batch := time.NewTicker(streamBatchEvery)
	defer batch.Stop()
	ping := time.NewTicker(streamPingEvery)
	defer ping.Stop()
	pending := map[int]vehicleJSON{}
	for {
		select {
		case <-r.Context().Done():
			return
		case v := <-updates:
			pending[v.ID] = s.vehicleJSON(v)
		case <-batch.C:
			if len(pending) == 0 {
				continue
			}
			list := make([]vehicleJSON, 0, len(pending))
			for _, v := range pending {
				list = append(list, v)
			}
			clear(pending)
			if err := send("update", list); err != nil {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}
