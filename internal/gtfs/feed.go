package gtfs

import (
	"archive/zip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Stop struct {
	ID       string
	Name     string
	Lat, Lon float64
	Zone     string
	Platform string
	Parent   string
	Station  bool
}

type Route struct {
	ID        string
	ShortName string
	Type      int
	Color     string
	TextColor string
}

type StopTime struct {
	Stop   int32
	Seq    int32
	Arr    int32
	Dep    int32
	Pickup bool
	Drop   bool
}

type Trip struct {
	ID       string
	Route    int32
	Service  int32
	Headsign string
	Times    []StopTime
}

type Service struct {
	ID          string
	HasCalendar bool
	Weekdays    [7]bool
	Start, End  int
	Added       map[int]bool
	Removed     map[int]bool
}

func (s *Service) ActiveOn(day time.Time) bool {
	key := DateKey(day)
	if s.Removed[key] {
		return false
	}
	if s.Added[key] {
		return true
	}
	return s.HasCalendar && key >= s.Start && key <= s.End && s.Weekdays[day.Weekday()]
}

type Transfer struct {
	From, To int32
	MinTime  int32
}

type TripTransfer struct {
	FromTrip, ToTrip int32
	FromStop, ToStop int32
	MinTime          int32
	MaxWait          int32
}

type Feed struct {
	Stops         []Stop
	StopIndex     map[string]int32
	Routes        []Route
	Trips         []Trip
	Services      []Service
	Transfers     []Transfer
	TripTransfers []TripTransfer
}

const MaxUncompressed = 1 << 30

func DateKey(t time.Time) int {
	y, m, d := t.Date()
	return y*10000 + int(m)*100 + d
}

func Load(path string) (*Feed, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	files := make(map[string]*zip.File, len(zr.File))
	var total uint64
	for _, f := range zr.File {
		files[f.Name] = f
		total += f.UncompressedSize64
	}
	if total > MaxUncompressed {
		return nil, fmt.Errorf("gtfs: uncompressed size %d exceeds limit %d", total, uint64(MaxUncompressed))
	}

	p := &parser{
		files:      files,
		feed:       &Feed{StopIndex: map[string]int32{}},
		routeIdx:   map[string]int32{},
		serviceIdx: map[string]int32{},
		tripIdx:    map[string]int32{},
	}
	steps := []struct {
		name     string
		required bool
		fn       func(get func(string) string) error
	}{
		{"stops.txt", true, p.stop},
		{"routes.txt", true, p.route},
		{"calendar.txt", false, p.calendar},
		{"calendar_dates.txt", false, p.calendarDate},
		{"trips.txt", true, p.trip},
		{"stop_times.txt", true, p.stopTime},
		{"transfers.txt", false, p.transfer},
	}
	for _, s := range steps {
		if err := readCSV(files, s.name, s.required, s.fn); err != nil {
			return nil, err
		}
	}

	for i := range p.feed.Trips {
		t := p.feed.Trips[i].Times
		sort.Slice(t, func(a, b int) bool { return t[a].Seq < t[b].Seq })
	}
	if len(p.feed.Trips) == 0 {
		return nil, errors.New("gtfs: feed has no trips")
	}
	return p.feed, nil
}

type parser struct {
	files      map[string]*zip.File
	feed       *Feed
	routeIdx   map[string]int32
	serviceIdx map[string]int32
	tripIdx    map[string]int32
}

func (p *parser) stop(get func(string) string) error {
	lat, _ := strconv.ParseFloat(get("stop_lat"), 64)
	lon, _ := strconv.ParseFloat(get("stop_lon"), 64)
	s := Stop{
		ID:       get("stop_id"),
		Name:     get("stop_name"),
		Lat:      lat,
		Lon:      lon,
		Zone:     get("zone_id"),
		Platform: get("platform_code"),
		Parent:   get("parent_station"),
		Station:  get("location_type") == "1",
	}
	p.feed.StopIndex[s.ID] = int32(len(p.feed.Stops))
	p.feed.Stops = append(p.feed.Stops, s)
	return nil
}

func (p *parser) route(get func(string) string) error {
	typ, _ := strconv.Atoi(get("route_type"))
	r := Route{
		ID:        get("route_id"),
		ShortName: get("route_short_name"),
		Type:      typ,
		Color:     get("route_color"),
		TextColor: get("route_text_color"),
	}
	if r.ShortName == "" {
		r.ShortName = get("route_long_name")
	}
	p.routeIdx[r.ID] = int32(len(p.feed.Routes))
	p.feed.Routes = append(p.feed.Routes, r)
	return nil
}

func (p *parser) service(id string) *Service {
	if i, ok := p.serviceIdx[id]; ok {
		return &p.feed.Services[i]
	}
	p.serviceIdx[id] = int32(len(p.feed.Services))
	p.feed.Services = append(p.feed.Services, Service{ID: id, Added: map[int]bool{}, Removed: map[int]bool{}})
	return &p.feed.Services[len(p.feed.Services)-1]
}

func (p *parser) calendar(get func(string) string) error {
	s := p.service(get("service_id"))
	s.HasCalendar = true
	days := []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}
	for i, d := range days {
		s.Weekdays[i] = get(d) == "1"
	}
	s.Start, _ = strconv.Atoi(get("start_date"))
	s.End, _ = strconv.Atoi(get("end_date"))
	return nil
}

func (p *parser) calendarDate(get func(string) string) error {
	s := p.service(get("service_id"))
	date, err := strconv.Atoi(get("date"))
	if err != nil {
		return err
	}
	switch get("exception_type") {
	case "1":
		s.Added[date] = true
	case "2":
		s.Removed[date] = true
	}
	return nil
}

func (p *parser) trip(get func(string) string) error {
	route, ok := p.routeIdx[get("route_id")]
	if !ok {
		return nil
	}
	svc := get("service_id")
	if _, ok := p.serviceIdx[svc]; !ok {
		p.service(svc)
	}
	t := Trip{ID: get("trip_id"), Route: route, Service: p.serviceIdx[svc], Headsign: get("trip_headsign")}
	p.tripIdx[t.ID] = int32(len(p.feed.Trips))
	p.feed.Trips = append(p.feed.Trips, t)
	return nil
}

func (p *parser) stopTime(get func(string) string) error {
	ti, ok := p.tripIdx[get("trip_id")]
	if !ok {
		return nil
	}
	si, ok := p.feed.StopIndex[get("stop_id")]
	if !ok {
		return nil
	}
	seq, _ := strconv.Atoi(get("stop_sequence"))
	arr, errA := parseTime(get("arrival_time"))
	dep, errD := parseTime(get("departure_time"))
	switch {
	case errA != nil && errD != nil:
		return fmt.Errorf("trip %s: missing times", get("trip_id"))
	case errA != nil:
		arr = dep
	case errD != nil:
		dep = arr
	}
	trip := &p.feed.Trips[ti]
	trip.Times = append(trip.Times, StopTime{
		Stop:   si,
		Seq:    int32(seq),
		Arr:    arr,
		Dep:    dep,
		Pickup: get("pickup_type") != "1",
		Drop:   get("drop_off_type") != "1",
	})
	return nil
}

func (p *parser) transfer(get func(string) string) error {
	if get("transfer_type") == "3" || get("from_route_id") != "" || get("to_route_id") != "" {
		return nil
	}
	from, ok1 := p.feed.StopIndex[get("from_stop_id")]
	to, ok2 := p.feed.StopIndex[get("to_stop_id")]
	if !ok1 || !ok2 {
		return nil
	}
	mt, _ := strconv.Atoi(get("min_transfer_time"))
	fromTrip, toTrip := get("from_trip_id"), get("to_trip_id")
	if fromTrip != "" || toTrip != "" {
		ft, ok1 := p.tripIdx[fromTrip]
		tt, ok2 := p.tripIdx[toTrip]
		if !ok1 || !ok2 || ft == tt {
			return nil
		}
		mw, _ := strconv.Atoi(get("max_waiting_time"))
		p.feed.TripTransfers = append(p.feed.TripTransfers, TripTransfer{FromTrip: ft, ToTrip: tt, FromStop: from, ToStop: to, MinTime: int32(mt), MaxWait: int32(mw)})
		return nil
	}
	if from == to {
		return nil
	}
	p.feed.Transfers = append(p.feed.Transfers, Transfer{From: from, To: to, MinTime: int32(mt)})
	return nil
}

func parseTime(s string) (int32, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("bad time %q", s)
	}
	var v [3]int
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, fmt.Errorf("bad time %q", s)
		}
		v[i] = n
	}
	return int32(v[0]*3600 + v[1]*60 + v[2]), nil
}

func readCSV(files map[string]*zip.File, name string, required bool, fn func(get func(string) string) error) error {
	f, ok := files[name]
	if !ok {
		if required {
			return fmt.Errorf("gtfs: missing %s", name)
		}
		return nil
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	r := csv.NewReader(rc)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = true

	header, err := r.Read()
	if err != nil {
		return fmt.Errorf("gtfs %s: %w", name, err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[strings.TrimSpace(strings.TrimPrefix(h, string(rune(0xFEFF))))] = i
	}

	var rec []string
	get := func(k string) string {
		i, ok := col[k]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	for {
		rec, err = r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("gtfs %s: %w", name, err)
		}
		if err := fn(get); err != nil {
			return fmt.Errorf("gtfs %s: %w", name, err)
		}
	}
}
