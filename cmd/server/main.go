package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"
	_ "time/tzdata"

	"brnotransport/internal/gtfs"
	"brnotransport/internal/live"
	"brnotransport/internal/planner"
	"brnotransport/internal/web"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	cachePath := flag.String("cache", "data/gtfs.zip", "path of the cached GTFS feed")
	feedURL := flag.String("gtfs-url", "https://kordis-jmk.cz/gtfs/gtfs.zip", "GTFS feed URL")
	ttl := flag.Duration("ttl", 4*time.Hour, "GTFS cache lifetime")
	mock := flag.Bool("mock", false, "use mock planner instead of GTFS")
	liveURL := flag.String("live-url", live.DefaultURL, "realtime vehicle stream (empty disables live positions)")
	flag.Parse()

	loc, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var p planner.Planner
	var stops planner.StopDirectory
	var names web.StopNamer
	var trips web.TripResolver
	if *mock {
		ms := planner.MockStops()
		p, stops = planner.NewMockPlanner(ms), ms
	} else {
		gp := planner.NewGTFSPlanner(&gtfs.Cache{URL: *feedURL, Path: *cachePath, TTL: *ttl}, loc)
		if err := gp.Init(ctx); err != nil {
			log.Fatal(err)
		}
		go gp.Run(ctx)
		p, stops, names, trips = gp, gp, gp, gp
	}

	srv, err := web.NewServer(p, stops, loc)
	if err != nil {
		log.Fatal(err)
	}
	if *liveURL != "" {
		tracker := live.NewTracker(*liveURL)
		go tracker.Run(ctx)
		srv.SetLive(tracker, names, trips)
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx)
	}()
	log.Printf("listening on %s", *addr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
