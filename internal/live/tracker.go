package live

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	DefaultURL   = "wss://gis.brno.cz/geoevent/ws/services/Kordis_stream/StreamServer/subscribe?outSR=4326"
	readTimeout  = 90 * time.Second
	staleAfter   = 5 * time.Minute
	forgetAfter  = 15 * time.Minute
	maxBackoff   = time.Minute
	pruneEvery   = 2000
	healthyAfter = time.Minute
)

type Vehicle struct {
	ID        int
	Line      string
	Lat, Lon  float64
	Bearing   int
	Delay     int
	LastStop  int
	FinalStop int
	Course    string
	LowFloor  bool
	Updated   time.Time
	Gone      bool
}

const subscriberBuffer = 1024

type subscriber struct {
	lines map[string]bool
	ch    chan Vehicle
}

type Tracker struct {
	url       string
	mu        sync.RWMutex
	vehicles  map[int]Vehicle
	subs      map[*subscriber]struct{}
	connected atomic.Bool
}

func NewTracker(url string) *Tracker {
	return &Tracker{url: url, vehicles: map[int]Vehicle{}, subs: map[*subscriber]struct{}{}}
}

func (t *Tracker) Subscribe(lines map[string]bool) (<-chan Vehicle, func()) {
	s := &subscriber{lines: lines, ch: make(chan Vehicle, subscriberBuffer)}
	t.mu.Lock()
	t.subs[s] = struct{}{}
	t.mu.Unlock()
	return s.ch, func() {
		t.mu.Lock()
		delete(t.subs, s)
		t.mu.Unlock()
	}
}

func (t *Tracker) publishLocked(v Vehicle) {
	for s := range t.subs {
		if s.lines[v.Line] {
			select {
			case s.ch <- v:
			default:
			}
		}
	}
}

func (t *Tracker) Connected() bool { return t.connected.Load() }

func (t *Tracker) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := t.stream(ctx)
		t.connected.Store(false)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > healthyAfter {
			backoff = time.Second
		}
		log.Printf("live: stream ended (%v), reconnecting in %v", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

type message struct {
	Attributes struct {
		ID          float64 `json:"ID"`
		Lat         float64 `json:"Lat"`
		Lng         float64 `json:"Lng"`
		Bearing     float64 `json:"Bearing"`
		LineName    string  `json:"LineName"`
		Delay       float64 `json:"Delay"`
		LastStopID  float64 `json:"LastStopID"`
		FinalStopID float64 `json:"FinalStopID"`
		Course      string  `json:"Course"`
		LF          string  `json:"LF"`
		IsInactive  string  `json:"IsInactive"`
		TimeUpdated float64 `json:"TimeUpdated"`
	} `json:"attributes"`
}

func (t *Tracker) stream(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	c, _, err := websocket.Dial(dialCtx, t.url, nil)
	cancel()
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	t.connected.Store(true)
	log.Printf("live: connected to vehicle stream")

	for n := 1; ; n++ {
		readCtx, cancel := context.WithTimeout(ctx, readTimeout)
		_, data, err := c.Read(readCtx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return errors.New("no data received")
			}
			return err
		}
		var m message
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		t.store(m)
		if n%pruneEvery == 0 {
			t.prune()
		}
	}
}

func (t *Tracker) store(m message) {
	a := m.Attributes
	if a.ID == 0 {
		return
	}
	id := int(a.ID)
	if a.IsInactive == "true" || a.LineName == "" {
		t.mu.Lock()
		if old, ok := t.vehicles[id]; ok {
			delete(t.vehicles, id)
			t.publishLocked(Vehicle{ID: id, Line: old.Line, Gone: true})
		}
		t.mu.Unlock()
		return
	}
	updated := time.Now()
	if a.TimeUpdated > 0 {
		updated = time.UnixMilli(int64(a.TimeUpdated))
	}
	v := Vehicle{
		ID:        id,
		Line:      a.LineName,
		Lat:       a.Lat,
		Lon:       a.Lng,
		Bearing:   int(a.Bearing),
		Delay:     int(a.Delay),
		LastStop:  int(a.LastStopID),
		FinalStop: int(a.FinalStopID),
		Course:    a.Course,
		LowFloor:  a.LF == "true",
		Updated:   updated,
	}
	t.mu.Lock()
	if old, ok := t.vehicles[id]; ok && old.Line != v.Line {
		t.publishLocked(Vehicle{ID: id, Line: old.Line, Gone: true})
	}
	t.vehicles[id] = v
	t.publishLocked(v)
	t.mu.Unlock()
}

func (t *Tracker) prune() {
	cutoff := time.Now().Add(-forgetAfter)
	t.mu.Lock()
	for id, v := range t.vehicles {
		if v.Updated.Before(cutoff) {
			delete(t.vehicles, id)
			t.publishLocked(Vehicle{ID: id, Line: v.Line, Gone: true})
		}
	}
	t.mu.Unlock()
}

func (t *Tracker) Vehicles(lines map[string]bool) []Vehicle {
	cutoff := time.Now().Add(-staleAfter)
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []Vehicle
	for _, v := range t.vehicles {
		if lines[v.Line] && v.Updated.After(cutoff) {
			out = append(out, v)
		}
	}
	return out
}
