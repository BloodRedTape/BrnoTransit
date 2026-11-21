package web

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"math"
	"net/url"
	"regexp"
	"time"

	"brnotransport/internal/model"
)

const (
	maxTokenLen   = 16 << 10
	maxMapLegs    = 16
	maxLegPoints  = 400
	coordDecimals = 1e5
)

var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type mapLeg struct {
	Walk      bool         `json:"w,omitempty"`
	Mode      string       `json:"m,omitempty"`
	Line      string       `json:"l,omitempty"`
	Color     string       `json:"c,omitempty"`
	TextColor string       `json:"tc,omitempty"`
	Headsign  string       `json:"h,omitempty"`
	From      string       `json:"f"`
	To        string       `json:"t"`
	Dep       string       `json:"d"`
	Arr       string       `json:"a"`
	Path      [][2]float64 `json:"p"`
	Trip      string       `json:"ti,omitempty"`
	BoardStop string       `json:"bs,omitempty"`
	BoardUnix int64        `json:"bt,omitempty"`
	Shape     [][2]float64 `json:"sh,omitempty"`
}

type mapPage struct {
	Title    string
	BackURL  string
	From     string
	To       string
	Dep      string
	Arr      string
	Duration int
	Legs     []mapLegView
	MapJSON  string
}

type mapLegView struct {
	mapLeg
	Minutes  int
	ColorCSS template.CSS
	TextCSS  template.CSS
}

func mapURL(j model.Journey, loc *time.Location) string {
	legs := make([]mapLeg, 0, len(j.Legs))
	for _, l := range j.Legs {
		if len(l.Path) < 2 {
			return ""
		}
		ml := mapLeg{
			Walk:      l.IsWalk(),
			Mode:      string(l.Mode),
			Line:      l.Line.Name,
			Color:     l.Line.Color,
			TextColor: l.Line.TextColor,
			Headsign:  l.Headsign,
			From:      l.From.Name,
			To:        l.To.Name,
			Dep:       l.Depart.In(loc).Format("15:04"),
			Arr:       l.Arrive.In(loc).Format("15:04"),
		}
		if !l.IsWalk() && l.TripID != "" {
			ml.Trip, ml.BoardStop, ml.BoardUnix = l.TripID, l.From.ID, l.Depart.Unix()
		}
		for _, p := range l.Path {
			ml.Path = append(ml.Path, [2]float64{round5(p.Lat), round5(p.Lon)})
		}
		legs = append(legs, ml)
	}
	b, err := json.Marshal(legs)
	if err != nil {
		return ""
	}
	return "/map?" + url.Values{"j": {base64.RawURLEncoding.EncodeToString(b)}}.Encode()
}

func decodeMapToken(token string) ([]mapLeg, error) {
	if token == "" || len(token) > maxTokenLen {
		return nil, errors.New("bad map token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	var legs []mapLeg
	if err := json.Unmarshal(raw, &legs); err != nil {
		return nil, err
	}
	if len(legs) == 0 || len(legs) > maxMapLegs {
		return nil, errors.New("bad map token")
	}
	for i := range legs {
		l := &legs[i]
		l.Shape = nil
		if len(l.Path) < 2 || len(l.Path) > maxLegPoints {
			return nil, errors.New("bad map token")
		}
		for _, p := range l.Path {
			if math.Abs(p[0]) > 90 || math.Abs(p[1]) > 180 {
				return nil, errors.New("bad map token")
			}
		}
		if !hexColorRe.MatchString(l.Color) {
			l.Color = "#4a8fdb"
		}
		if !hexColorRe.MatchString(l.TextColor) {
			l.TextColor = "#ffffff"
		}
	}
	return legs, nil
}

func buildMapPage(legs []mapLeg) (mapPage, error) {
	b, err := json.Marshal(legs)
	if err != nil {
		return mapPage{}, err
	}
	first, last := legs[0], legs[len(legs)-1]
	p := mapPage{
		Title:    "Route map",
		BackURL:  "/search?" + url.Values{"from": {first.From}, "to": {last.To}}.Encode(),
		From:     first.From,
		To:       last.To,
		Dep:      first.Dep,
		Arr:      last.Arr,
		Duration: minutesBetween(first.Dep, last.Arr),
		MapJSON:  string(b),
	}
	for _, l := range legs {
		p.Legs = append(p.Legs, mapLegView{
			mapLeg:   l,
			Minutes:  minutesBetween(l.Dep, l.Arr),
			ColorCSS: template.CSS(l.Color),
			TextCSS:  template.CSS(l.TextColor),
		})
	}
	return p, nil
}

func minutesBetween(a, b string) int {
	ta, err1 := time.Parse("15:04", a)
	tb, err2 := time.Parse("15:04", b)
	if err1 != nil || err2 != nil {
		return 0
	}
	d := int(tb.Sub(ta).Minutes())
	if d < 0 {
		d += 24 * 60
	}
	return d
}

func round5(v float64) float64 { return math.Round(v*coordDecimals) / coordDecimals }
