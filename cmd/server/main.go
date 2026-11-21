package main

import (
	"flag"
	"log"
	"net/http"
	"time"
	_ "time/tzdata"

	"brnotransport/internal/planner"
	"brnotransport/internal/web"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	flag.Parse()

	loc, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		log.Fatal(err)
	}

	stops := planner.MockStops()
	srv, err := web.NewServer(planner.NewMockPlanner(stops), stops, loc)
	if err != nil {
		log.Fatal(err)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("listening on %s", *addr)
	log.Fatal(httpSrv.ListenAndServe())
}
