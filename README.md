# Brno Transit

A small, server-rendered journey planner for Brno public transport (IDS JMK).
It finds the connection that gets you to your destination **as early as
possible**, starting from right now.

The official DPMB app sorts connections by time spent on board, which helps
with time-limited tickets. This app sorts by **earliest arrival** instead.

## Features

- **Search screen:** from/to stop inputs with an accent-insensitive
  autocomplete. `Kra`, `Krá` and `kralovo` all find *Královo Pole*; the
  server also accepts names without diacritics.
- **Results screen:** up to ~6 connections sorted by arrival time (the sort is
  fixed). Each card shows line badges in official colours, platforms,
  intermediate stop count and walking transfers.
- **Map screen** (`/map`): the route on a vector map, coloured per line, with
  start/end markers and transfer points.
  - The whole journey is encoded in the link, so map links keep working after
    a server restart.
  - Optional **Live** switch shows the real-time position of the **exact
    vehicles you will ride** in this plan, one per leg. It also shows a status
    line per leg: delay, "trip hasn't started yet, leaves X at HH:MM", or "no
    live position yet". Updates are pushed via Server-Sent Events (see
    [Live vehicles](#live-vehicles)).
- **PWA:** installable, with a service worker, an offline fallback page and
  cache-busted assets.
- **Light/dark theme** following the system setting, including the map style.

## Running

Requires Go 1.27+.

```sh
go run ./cmd/server
```

Then open <http://localhost:8080>.

On first start the GTFS feed (~6 MB) is downloaded to `data/gtfs.zip`.

| Flag        | Default                                      | Description                                             |
|-------------|----------------------------------------------|---------------------------------------------------------|
| `-addr`     | `:8080`                                      | HTTP listen address                                     |
| `-cache`    | `data/gtfs.zip`                              | Path of the cached GTFS feed                            |
| `-gtfs-url` | `https://kordis-jmk.cz/gtfs/gtfs.zip`        | GTFS feed URL                                           |
| `-ttl`      | `4h`                                         | GTFS cache lifetime                                     |
| `-live-url` | KORDIS stream (see below)                    | Real-time vehicle stream; empty string disables it      |
| `-mock`     | `false`                                      | Use the mock planner with fake data (UI work offline)   |

Service workers need HTTPS except on `localhost`. To test the PWA on a phone,
put the server behind HTTPS (a reverse proxy or a tunnel).

## Data sources

| What                    | Source                                                                                          |
|-------------------------|-------------------------------------------------------------------------------------------------|
| Timetables              | GTFS feed from KORDIS JMK (also listed on data.brno.cz)                                          |
| Real-time positions     | ArcGIS GeoEvent WebSocket `wss://gis.brno.cz/geoevent/ws/services/Kordis_stream/StreamServer/subscribe` |
| Base map                | [VersaTiles](https://versatiles.org) vector tiles (`colorful` / `colorful-dark`), © OpenStreetMap contributors |
| Map renderer            | MapLibre GL JS 5.24.1 from unpkg (loaded with SRI)                                              |

Notes:
- The feed has no `shapes.txt`. Route lines on the map are therefore straight
  segments between consecutive stops.
- The real-time stream sends every vehicle's position once every **~10
  seconds**, in batches. Live markers update at that rate.
- `LastStopID` / `FinalStopID` in the stream are the numeric part of GTFS stop
  IDs (`U1559Z2` → `1559`, i.e. the station). This is what links a live
  vehicle to a GTFS trip.
- The stream's `Course` (vehicle duty) cannot be linked to trips because
  `block_id` is empty in the GTFS feed. A vehicle can therefore only be
  identified once it is actually running the trip.

## How routing works

Routing uses **RAPTOR** (Round-bAsed Public Transit Optimized Router).

- **Feed loading** (`internal/gtfs`)
  - The cached zip is reused while it is younger than the TTL.
  - A new download is parsed first and only then replaces the old file.
  - If a download fails, the stale cache is used.
  - A background loop checks the TTL every 10 minutes and hot-swaps the
    dataset.
- **Network** (`internal/raptor/network.go`)
  - Trips are grouped into *patterns*: same route, same stop sequence, same
    pickup/drop-off flags.
  - Footpaths come from `transfers.txt` (stop-to-stop rows only), plus
    generated walks between any stops within 400 m. Walking speed is 1.3 m/s
    with a 1.25 detour factor, minimum 60 s.
- **Daily timetable** (`timetable.go`) is built per service date and cached.
  - It includes the previous day's trips that run past midnight (times ≥ 24:00)
    and next-day trips departing before 08:00.
  - Patterns are split into FIFO routes, so that trips never overtake each
    other.
- **Search** (`search.go`) runs standard RAPTOR rounds with target pruning. It
  returns a Pareto set of (arrival time, number of rides) for one departure
  time.
  - Up to 6 rides per journey.
  - A **transfer slack of 1 minute** is required on top of walking time. It
    applies only after a ride; the first boarding has no slack.
- **Planner** (`internal/planner/gtfs.go`)
  - Repeats the search, each time departing one minute after the previous
    best departure. It stops after ~6 distinct journeys or a 3-hour window.
  - Search time is truncated to the current minute, so a vehicle departing
    this minute is still offered.
  - Exact duplicates are dropped. Journeys that use the same vehicles and
    arrive at the same time but leave earlier (a pointless walk to an earlier
    stop of the same trip) are merged.
- **Sorting** by earliest arrival happens in the web layer
  (`web.SortByEarliestArrival`).

A typical query takes 5–40 ms.

## Live vehicles

`internal/live.Tracker` keeps one WebSocket connection to the KORDIS stream.
It reconnects with exponential backoff and holds the latest state of every
vehicle in memory. Vehicles silent for 5 minutes are hidden, and forgotten
after 15 minutes. Subscribers receive updates for their lines via pub/sub.

**Matching a vehicle to a trip.** Every ride leg in a map link carries its GTFS
`trip_id`, boarding stop and departure time. From these the server rebuilds
the trip's full schedule with absolute times (`planner.TripSchedule`). A
vehicle matches the trip when all of these hold:

- same line;
- its `LastStopID` is a station of the trip;
- the vehicle's timestamp minus (scheduled departure from that station + the
  vehicle's reported delay) falls within the window below.

| Where the vehicle is              | Accepted window                                    |
|-----------------------------------|----------------------------------------------------|
| Intermediate station              | −2 min … segment time to the next stop + 3 min     |
| Trip's first station              | up to 15 min early (layover at the terminus)       |

Vehicles of the same line running earlier or later trips are a headway away
in time, so they fall outside the window. Opposite-direction vehicles do not
pass the trip's stations at those times either. When several vehicles match,
the closest one in time wins.

Statuses per leg:

| Status        | Meaning                                                                 |
|---------------|-------------------------------------------------------------------------|
| `live`        | A matching vehicle was found; its position and delay are shown          |
| `not_started` | The trip has not started yet; reports its first stop and departure time |
| `waiting`     | The trip should be running, but no vehicle matched yet                  |
| `unknown`     | The trip could not be resolved (e.g. the GTFS feed was refreshed)       |

The SSE endpoint recomputes the state at most every 300 ms when updates
arrive, and at least every 20 s (together with a keep-alive ping). It sends
an event only when the state changes.

## Project layout

```
cmd/server/            entry point, flags, wiring
internal/gtfs/         GTFS zip parser and file cache with TTL
internal/raptor/       network, per-day timetable, RAPTOR search
internal/planner/      Planner/StopDirectory interfaces, GTFS planner, mock planner
internal/live/         WebSocket client for the real-time stream, in-memory state, pub/sub
internal/model/        Stop, Line, Leg, Journey
internal/web/          HTTP handlers, view models, map link encoding, SSE, PWA
  templates/           html/template pages (layout, index, results, map, offline)
  static/              style.css, app.js, manifest, icons (embedded)
  pwa/sw.js            service worker template (versioned per build)
```

All templates and static files are embedded in the binary.

### HTTP endpoints

| Route                         | Purpose                                                    |
|-------------------------------|------------------------------------------------------------|
| `GET /`                       | Search screen                                              |
| `GET /search?from=&to=`       | Results                                                    |
| `GET /map?j=`                 | Map of one journey (`j` = base64url JSON of the legs)      |
| `GET /api/vehicles?lines=`    | Snapshot of all live vehicles of the given lines (JSON, for debugging) |
| `GET /api/vehicles/stream?leg=TRIP~STOP~UNIX&leg=…` | SSE: `state` events with the matched vehicle and status per leg |
| `GET /sw.js`, `/manifest.webmanifest`, `/offline` | PWA plumbing                          |

## Known limitations / ideas

- No real-time data in routing yet. Cards show "Scheduled". The stream's
  `Delay` could feed into arrival times and transfer feasibility.
- Route lines on the map are straight segments between stops (no track/road
  geometry).
- Live markers jump every ~10 s. Smooth interpolation or dead-reckoning along
  the route is a planned next step.
- The vehicle for a trip that has not started yet cannot be predicted (no
  `block_id` in GTFS). It only shows up once it reaches the trip's first stop
  (up to 15 min before departure) or is running the trip.
- Optional filter not implemented: hide journeys with the same arrival but an
  earlier departure.
- The PWA splash background is dark in both themes (the manifest cannot
  follow the system theme).
