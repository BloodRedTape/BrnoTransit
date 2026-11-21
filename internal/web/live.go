package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"brnotransport/internal/live"
)

const maxLiveLines = 20

type StopNamer interface {
	StopNameByNumber(n int) (string, bool)
}

type vehicleJSON struct {
	ID       int     `json:"id"`
	Line     string  `json:"line"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Bearing  int     `json:"bearing"`
	Delay    int     `json:"delay"`
	Final    string  `json:"final"`
	LowFloor bool    `json:"lowFloor"`
	Updated  int64   `json:"updated"`
}

func (s *Server) SetLive(t *live.Tracker, names StopNamer) {
	s.live, s.stopNames = t, names
}

func (s *Server) handleVehicles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.live == nil {
		http.Error(w, "live data disabled", http.StatusServiceUnavailable)
		return
	}
	lines := map[string]bool{}
	for _, l := range strings.Split(r.URL.Query().Get("lines"), ",") {
		if l = strings.TrimSpace(l); l != "" && len(lines) < maxLiveLines {
			lines[l] = true
		}
	}
	if len(lines) == 0 {
		http.Error(w, "no lines requested", http.StatusBadRequest)
		return
	}
	if !s.live.Connected() && len(s.live.Vehicles(lines)) == 0 {
		http.Error(w, "live data unavailable", http.StatusServiceUnavailable)
		return
	}
	out := []vehicleJSON{}
	for _, v := range s.live.Vehicles(lines) {
		vj := vehicleJSON{ID: v.ID, Line: v.Line, Lat: v.Lat, Lon: v.Lon, Bearing: v.Bearing, Delay: v.Delay, LowFloor: v.LowFloor, Updated: v.Updated.Unix()}
		if s.stopNames != nil {
			vj.Final, _ = s.stopNames.StopNameByNumber(v.FinalStop)
		}
		out = append(out, vj)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
