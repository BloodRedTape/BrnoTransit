package planner

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"strconv"
	"time"

	"brnotransport/internal/model"
)

var mockStopNames = []string{
	"Hlavní nádraží",
	"Česká",
	"Moravské náměstí",
	"Mendlovo náměstí",
	"Zemědělská",
	"Haškova",
	"Královo Pole",
	"Malinovského náměstí",
	"Náměstí Svobody",
	"Hybešova",
	"Semilasso",
	"Úzká",
	"Vozovna Komín",
	"Starý Lískovec",
	"Bohunice",
	"Lesná",
	"Řečkovice",
	"Komárov",
	"Židenice",
	"Tábor",
	"Konečného náměstí",
}

func MockStops() *StaticStops {
	stops := make([]model.Stop, len(mockStopNames))
	for i, n := range mockStopNames {
		stops[i] = model.Stop{ID: "S" + strconv.Itoa(1000+i), Name: n}
	}
	return NewStaticStops(stops)
}

type mockLine struct {
	mode  model.Mode
	line  model.Line
	speed int
}

var mockLines = []mockLine{
	{model.Tram, model.Line{Name: "1", Color: "#d4262d"}, 380},
	{model.Tram, model.Line{Name: "4", Color: "#f39200"}, 360},
	{model.Tram, model.Line{Name: "9", Color: "#8e4a9e"}, 370},
	{model.Tram, model.Line{Name: "12", Color: "#00a19a"}, 350},
	{model.Bus, model.Line{Name: "46", Color: "#2f86d0"}, 420},
	{model.Bus, model.Line{Name: "67", Color: "#2f86d0"}, 430},
	{model.Trolleybus, model.Line{Name: "25", Color: "#2e9e4f"}, 340},
	{model.Trolleybus, model.Line{Name: "32", Color: "#2e9e4f"}, 330},
}

type MockPlanner struct {
	stops StopDirectory
}

func NewMockPlanner(stops StopDirectory) *MockPlanner {
	return &MockPlanner{stops: stops}
}

func (m *MockPlanner) Plan(ctx context.Context, from, to string, depart time.Time) ([]model.Journey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	src, ok := m.stops.Lookup(from)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownStop, from)
	}
	dst, ok := m.stops.Lookup(to)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownStop, to)
	}

	h := fnv.New64a()
	h.Write([]byte(src.ID + "|" + dst.ID))
	seed := h.Sum64()
	rng := rand.New(rand.NewPCG(seed, seed>>7))

	base := depart.Truncate(time.Minute)
	offsets := []int{3, 7, 12, 18, 26}
	journeys := make([]model.Journey, 0, len(offsets))
	for i, off := range offsets {
		start := base.Add(time.Duration(off+rng.IntN(3)) * time.Minute)
		if i%2 == 1 {
			journeys = append(journeys, m.transferJourney(rng, src, dst, start))
		} else {
			journeys = append(journeys, directJourney(rng, src, dst, start))
		}
	}
	return journeys, nil
}

func directJourney(rng *rand.Rand, src, dst model.Stop, start time.Time) model.Journey {
	ml := mockLines[rng.IntN(len(mockLines))]
	return model.Journey{Legs: []model.Leg{rideLeg(rng, ml, src, dst, start, 12+rng.IntN(16))}}
}

func (m *MockPlanner) transferJourney(rng *rand.Rand, src, dst model.Stop, start time.Time) model.Journey {
	mid := m.pickTransferStop(rng, src, dst)
	l1 := mockLines[rng.IntN(len(mockLines))]
	l2 := mockLines[rng.IntN(len(mockLines))]
	for l2.line.Name == l1.line.Name {
		l2 = mockLines[rng.IntN(len(mockLines))]
	}

	first := rideLeg(rng, l1, src, mid, start, 5+rng.IntN(8))
	walkMin := 1 + rng.IntN(3)
	walkStart := first.Arrive.Add(time.Duration(first.DelayMinutes) * time.Minute)
	walk := model.Leg{
		Mode:           model.Walk,
		From:           mid,
		To:             mid,
		FromPlatform:   first.ToPlatform,
		Depart:         walkStart,
		Arrive:         walkStart.Add(time.Duration(walkMin) * time.Minute),
		DistanceMeters: walkMin * 75,
	}
	second := rideLeg(rng, l2, mid, dst, walk.Arrive.Add(time.Duration(rng.IntN(4))*time.Minute), 5+rng.IntN(8))
	walk.ToPlatform = second.FromPlatform
	return model.Journey{Legs: []model.Leg{first, walk, second}}
}

func (m *MockPlanner) pickTransferStop(rng *rand.Rand, src, dst model.Stop) model.Stop {
	all := m.stops.Stops()
	for {
		s := all[rng.IntN(len(all))]
		if s.ID != src.ID && s.ID != dst.ID {
			return s
		}
	}
}

func rideLeg(rng *rand.Rand, ml mockLine, from, to model.Stop, dep time.Time, minutes int) model.Leg {
	delay := 0
	if rng.IntN(3) == 0 {
		delay = 1 + rng.IntN(3)
	}
	return model.Leg{
		Mode:              ml.mode,
		Line:              ml.line,
		From:              from,
		To:                to,
		FromPlatform:      strconv.Itoa(1 + rng.IntN(4)),
		ToPlatform:        strconv.Itoa(1 + rng.IntN(4)),
		Depart:            dep,
		Arrive:            dep.Add(time.Duration(minutes) * time.Minute),
		IntermediateStops: max(0, minutes*2/3-1),
		DelayMinutes:      delay,
		DistanceMeters:    minutes * ml.speed,
	}
}
