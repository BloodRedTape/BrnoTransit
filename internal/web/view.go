package web

import (
	"fmt"
	"html/template"
	"math"
	"sort"
	"time"

	"brnotransport/internal/model"
)

const soonThreshold = 10

var funcs = template.FuncMap{
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	},
}

func SortByEarliestArrival(js []model.Journey) {
	sort.SliceStable(js, func(i, k int) bool {
		ai, ak := js[i].Arrival(), js[k].Arrival()
		if !ai.Equal(ak) {
			return ai.Before(ak)
		}
		return js[i].Departure().Before(js[k].Departure())
	})
}

type resultsPage struct {
	Title    string
	From     string
	To       string
	BackURL  string
	SortedBy string
	Journeys []journeyView
}

type journeyView struct {
	InMinutes   int
	Soon        bool
	DurationMin int
	DistanceKm  string
	Legs        []legView
	MapURL      string
}

type legView struct {
	Walk         bool
	Mode         string
	Line         string
	Color        template.CSS
	TextColor    template.CSS
	Headsign     string
	DepartTime   string
	ArriveTime   string
	From         string
	To           string
	FromPlatform string
	ToPlatform   string
	Intermediate int
	DelayMinutes int
	WalkMinutes  int
	Guaranteed   bool
	WaitMinutes  int
}

func buildJourneyViews(js []model.Journey, now time.Time, loc *time.Location, signer mapSigner) []journeyView {
	out := make([]journeyView, 0, len(js))
	for _, j := range js {
		in := int(math.Ceil(j.Departure().Sub(now).Minutes()))
		if in < 0 {
			in = 0
		}
		jv := journeyView{
			InMinutes:   in,
			Soon:        in <= soonThreshold,
			DurationMin: int(j.Duration().Round(time.Minute).Minutes()),
			DistanceKm:  formatKm(j.DistanceMeters()),
			MapURL:      signer.mapURL(j, loc),
		}
		for _, l := range j.Legs {
			jv.Legs = append(jv.Legs, legView{
				Walk:         l.IsWalk(),
				Mode:         string(l.Mode),
				Line:         l.Line.Name,
				Color:        template.CSS(l.Line.Color),
				TextColor:    template.CSS(orDefault(l.Line.TextColor, "#ffffff")),
				Headsign:     l.Headsign,
				DepartTime:   l.Depart.In(loc).Format("15:04"),
				ArriveTime:   l.Arrive.In(loc).Format("15:04"),
				From:         l.From.Name,
				To:           l.To.Name,
				FromPlatform: l.FromPlatform,
				ToPlatform:   l.ToPlatform,
				Intermediate: l.IntermediateStops,
				DelayMinutes: l.DelayMinutes,
				WalkMinutes:  int(l.Duration().Round(time.Minute).Minutes()),
				Guaranteed:   l.Guaranteed,
				WaitMinutes:  int(math.Ceil(l.MaxWait.Minutes())),
			})
		}
		out = append(out, jv)
	}
	return out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func formatKm(m int) string {
	km := float64(m) / 1000
	if km < 10 {
		return fmt.Sprintf("%.1f km", km)
	}
	return fmt.Sprintf("%.0f km", km)
}
