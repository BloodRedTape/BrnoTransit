package web

import (
	"math"
	"strconv"
	"strings"

	"brnotransport/internal/model"
)

func parseHere(s string) (*model.Point, bool) {
	a, b, ok := strings.Cut(s, ",")
	if !ok {
		return nil, false
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
	lon, err2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if err1 != nil || err2 != nil || math.IsNaN(lat) || math.IsNaN(lon) || math.Abs(lat) > 90 || math.Abs(lon) > 180 {
		return nil, false
	}
	return &model.Point{Lat: lat, Lon: lon}, true
}

func formatHere(p model.Point) string {
	return strconv.FormatFloat(p.Lat, 'f', -1, 64) + "," + strconv.FormatFloat(p.Lon, 'f', -1, 64)
}
