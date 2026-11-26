package planner

import (
	"context"
	"errors"
	"strings"
	"time"

	"brnotransport/internal/model"
)

var ErrUnknownStop = errors.New("unknown stop")

const OriginName = "My location"

const (
	DefaultCount = 6
	MaxCount     = 30
)

type Options struct {
	Alternatives bool
	Origin       *model.Point
	Count        int
}

func (o Options) count() int {
	if o.Count <= 0 {
		return DefaultCount
	}
	return min(o.Count, MaxCount)
}

type Planner interface {
	Plan(ctx context.Context, from, to string, depart time.Time, opts Options) ([]model.Journey, error)
}

type StopDirectory interface {
	Stops() []model.Stop
	Lookup(name string) (model.Stop, bool)
}

type StaticStops struct {
	stops  []model.Stop
	byName map[string]model.Stop
}

func NewStaticStops(stops []model.Stop) *StaticStops {
	s := &StaticStops{stops: stops, byName: make(map[string]model.Stop, len(stops))}
	for _, st := range stops {
		s.byName[normalize(st.Name)] = st
	}
	return s
}

func (s *StaticStops) Stops() []model.Stop { return s.stops }

func (s *StaticStops) Lookup(name string) (model.Stop, bool) {
	st, ok := s.byName[normalize(name)]
	return st, ok
}

func normalize(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}
