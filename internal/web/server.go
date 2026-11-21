package web

import (
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"brnotransport/internal/live"
	"brnotransport/internal/planner"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	planner planner.Planner
	stops   planner.StopDirectory
	loc     *time.Location
	now     func() time.Time
	pages   map[string]*template.Template

	version       string
	serviceWorker []byte

	live      *live.Tracker
	stopNames StopNamer
}

func NewServer(p planner.Planner, stops planner.StopDirectory, loc *time.Location) (*Server, error) {
	s := &Server{planner: p, stops: stops, loc: loc, now: time.Now, pages: map[string]*template.Template{}}
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	if s.version, err = hashFS(static); err != nil {
		return nil, err
	}
	for _, page := range []string{"index.html", "results.html", "map.html", "offline.html"} {
		t, err := template.New("").Funcs(funcs).Funcs(template.FuncMap{"asset": s.asset}).ParseFS(templateFS, "templates/layout.html", "templates/icons.html", "templates/"+page)
		if err != nil {
			return nil, err
		}
		s.pages[page] = t
	}
	if err := s.buildServiceWorker(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /search", s.handleSearch)
	mux.HandleFunc("GET /map", s.handleMap)
	mux.HandleFunc("GET /offline", s.handleOffline)
	mux.HandleFunc("GET /api/vehicles", s.handleVehicles)
	mux.HandleFunc("GET /sw.js", s.handleServiceWorker)
	mux.HandleFunc("GET /manifest.webmanifest", s.handleManifest)
	mux.Handle("GET /favicon.ico", http.RedirectHandler(s.asset("icons/favicon-32.png"), http.StatusMovedPermanently))
	mux.Handle("GET /static/", staticCache(http.StripPrefix("/static/", http.FileServerFS(static))))
	return mux
}

type indexPage struct {
	Title string
	From  string
	To    string
	Error string
	Stops []string
}

func (s *Server) indexData(from, to, errMsg string) indexPage {
	names := make([]string, 0, len(s.stops.Stops()))
	for _, st := range s.stops.Stops() {
		names = append(names, st.Name)
	}
	return indexPage{
		Title: "Brno Transit",
		From:  from,
		To:    to,
		Error: errMsg,
		Stops: names,
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.render(w, http.StatusOK, "index.html", s.indexData(q.Get("from"), q.Get("to"), ""))
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from := strings.TrimSpace(q.Get("from"))
	to := strings.TrimSpace(q.Get("to"))

	if msg := s.validate(from, to); msg != "" {
		s.render(w, http.StatusBadRequest, "index.html", s.indexData(from, to, msg))
		return
	}

	now := s.now().In(s.loc)
	journeys, err := s.planner.Plan(r.Context(), from, to, now)
	if err != nil {
		if errors.Is(err, planner.ErrUnknownStop) {
			s.render(w, http.StatusBadRequest, "index.html", s.indexData(from, to, "Unknown stop."))
			return
		}
		log.Printf("plan %q -> %q: %v", from, to, err)
		http.Error(w, "planning failed", http.StatusInternalServerError)
		return
	}
	SortByEarliestArrival(journeys)

	fromStop, _ := s.stops.Lookup(from)
	toStop, _ := s.stops.Lookup(to)
	back := url.Values{"from": {fromStop.Name}, "to": {toStop.Name}}
	s.render(w, http.StatusOK, "results.html", resultsPage{
		Title:    "Connections found",
		From:     fromStop.Name,
		To:       toStop.Name,
		BackURL:  "/?" + back.Encode(),
		SortedBy: "Fastest arrival",
		Journeys: buildJourneyViews(journeys, now, s.loc),
	})
}

func (s *Server) handleMap(w http.ResponseWriter, r *http.Request) {
	legs, err := decodeMapToken(r.URL.Query().Get("j"))
	if err != nil {
		http.Error(w, "invalid route link", http.StatusBadRequest)
		return
	}
	page, err := buildMapPage(legs)
	if err != nil {
		http.Error(w, "invalid route link", http.StatusBadRequest)
		return
	}
	s.render(w, http.StatusOK, "map.html", page)
}

func (s *Server) validate(from, to string) string {
	if from == "" || to == "" {
		return "Please enter both stops."
	}
	f, ok := s.stops.Lookup(from)
	if !ok {
		return "Unknown stop: " + from
	}
	t, ok := s.stops.Lookup(to)
	if !ok {
		return "Unknown stop: " + to
	}
	if f.ID == t.ID {
		return "From and To must be different stops."
	}
	return ""
}

func (s *Server) render(w http.ResponseWriter, status int, page string, data any) {
	var buf strings.Builder
	if err := s.pages[page].ExecuteTemplate(&buf, "layout", data); err != nil {
		log.Printf("render %s: %v", page, err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(buf.String()))
}
