package model

import "time"

type Mode string

const (
	Tram       Mode = "tram"
	Bus        Mode = "bus"
	Trolleybus Mode = "trolleybus"
	Train      Mode = "train"
	Walk       Mode = "walk"
)

type Stop struct {
	ID   string
	Name string
}

type Line struct {
	Name      string
	Color     string
	TextColor string
}

type Leg struct {
	Mode              Mode
	Line              Line
	Headsign          string
	From              Stop
	To                Stop
	FromPlatform      string
	ToPlatform        string
	Depart            time.Time
	Arrive            time.Time
	IntermediateStops int
	DelayMinutes      int
	DistanceMeters    int
}

func (l Leg) IsWalk() bool { return l.Mode == Walk }

func (l Leg) Duration() time.Duration { return l.Arrive.Sub(l.Depart) }

type Journey struct {
	Legs []Leg
}

func (j Journey) Departure() time.Time {
	if len(j.Legs) == 0 {
		return time.Time{}
	}
	return j.Legs[0].Depart
}

func (j Journey) Arrival() time.Time {
	if len(j.Legs) == 0 {
		return time.Time{}
	}
	last := j.Legs[len(j.Legs)-1]
	return last.Arrive.Add(time.Duration(last.DelayMinutes) * time.Minute)
}

func (j Journey) Duration() time.Duration { return j.Arrival().Sub(j.Departure()) }

func (j Journey) DistanceMeters() int {
	total := 0
	for _, l := range j.Legs {
		total += l.DistanceMeters
	}
	return total
}

func (j Journey) Transfers() int {
	n := 0
	for _, l := range j.Legs {
		if !l.IsWalk() {
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return n - 1
}
