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
- **Results screen:** every distinct combination of trips (up to ~20), sorted by arrival time; on equal
  arrival, the one departing earlier comes first (the sort is fixed). Each card
  shows line badges in official colours, platforms, intermediate stop count,
  walking transfers and guaranteed connections. See
  [Which connections are shown](#which-connections-are-shown).
- **Map screen** (`/map`): the route on a vector map, coloured per line, with
  start/end markers and transfer points. Rides follow the real street/track
  geometry from the city's route layer (see [Route shapes](#route-shapes)).
  - The whole journey is encoded in the link, so map links keep working after
    a server restart.
  - Optional **Live** switch shows the real-time position of the **exact
    vehicles you will ride** in this plan, one per leg. It also shows a status
    line per leg: delay, "trip hasn't started yet, leaves X at HH:MM", or "no
    live position yet". While a vehicle is still on its way to your boarding
    stop, its remaining path is drawn at 40 % opacity. Updates are pushed via
    Server-Sent Events (see [Live vehicles](#live-vehicles)).
- **PWA:** installable, with a service worker, an offline fallback page and
  cache-busted assets.
- **Light/dark theme** following the system setting, including the map style.

## Running

Requires Go 1.27+.

```sh
go run ./cmd/server
```

Then open <http://localhost:8080>.

On first start two files are downloaded:
- the GTFS feed (~6 MB) to `data/gtfs.zip`;
- the route geometry (~2 MB) to `data/routes.geojson`.

| Flag            | Default                               | Description                                                   |
|-----------------|---------------------------------------|---------------------------------------------------------------|
| `-addr`         | `:8080`                               | HTTP listen address                                           |
| `-cache`        | `data/gtfs.zip`                       | Path of the cached GTFS feed                                  |
| `-gtfs-url`     | `https://kordis-jmk.cz/gtfs/gtfs.zip` | GTFS feed URL                                                 |
| `-ttl`          | `4h`                                  | GTFS cache lifetime                                           |
| `-shapes-url`   | city `transit_routes` layer (GeoJSON) | Route geometry source; empty string disables real shapes      |
| `-shapes-cache` | `data/routes.geojson`                 | Path of the cached route geometry                             |
| `-shapes-ttl`   | `168h` (7 days)                       | Route geometry cache lifetime                                 |
| `-live-url`     | KORDIS stream (see below)             | Real-time vehicle stream; empty string disables it            |
| `-mock`         | `false`                               | Use the mock planner with fake data (UI work offline)         |

Service workers need HTTPS except on `localhost`. To test the PWA on a phone,
put the server behind HTTPS (a reverse proxy or a tunnel).

### Docker

The image is a static binary on `gcr.io/distroless/static` (non-root). It
listens on plain HTTP `:8080` and keeps both caches in the `/data` volume.

With Compose (publishes the app on port **23461**):

```sh
docker compose up -d --build
```

Or plain Docker:

```sh
docker build -t brno-transit .
docker run -d --name brno-transit -p 23461:8080 -v brno-transit-data:/data brno-transit
```

To pass extra flags, override the entrypoint arguments (e.g. append
`-live-url ""` to disable live positions). The server shuts down cleanly on
`SIGTERM`, so `docker stop` closes SSE connections immediately.

Behind Caddy, which provides HTTPS and needs no special settings for SSE:

```
transit.example.com {
	reverse_proxy 127.0.0.1:23461
}
```

## Data sources

| What                    | Source                                                                                          |
|-------------------------|-------------------------------------------------------------------------------------------------|
| Timetables              | GTFS feed from KORDIS JMK (also listed on data.brno.cz)                                          |
| Real-time positions     | ArcGIS GeoEvent WebSocket `wss://gis.brno.cz/geoevent/ws/services/Kordis_stream/StreamServer/subscribe` |
| Route geometry          | City of Brno ArcGIS layer `transit_routes` (`services6.arcgis.com/fUWVlHWZNxUvTUh8/.../transit_routes/FeatureServer/0`), listed on data.brno.cz |
| Base map                | [VersaTiles](https://versatiles.org) vector tiles (`colorful` / `colorful-dark`), © OpenStreetMap contributors |
| Map renderer            | MapLibre GL JS 5.24.1 from unpkg (loaded with SRI)                                              |

Notes:
- The GTFS feed has no `shapes.txt`. Ride geometry therefore comes from the
  city's `transit_routes` layer and is matched to stops (see
  [Route shapes](#route-shapes)).
- The real-time stream sends every vehicle's position once every **~10
  seconds**, in batches. Live markers update at that rate.
- `LastStopID` / `FinalStopID` in the stream are the numeric part of GTFS stop
  IDs (`U1559Z2` → `1559`, i.e. the station). This is what links a live
  vehicle to a GTFS trip.
- The stream's `Course` (vehicle duty) cannot be linked to trips because
  `block_id` is empty in the GTFS feed. A vehicle can therefore only be
  identified once it is actually running the trip.

## Which connections are shown

**Terms.**
- A **trip** is one concrete run of a vehicle (a GTFS `trip_id`), e.g. the
  N92 leaving Haškova at 00:10. The line number alone is not a trip: the N92 at
  00:10 and the N92 at 00:40 are different trips.
- A **connection** is a set of trips plus the walks between them.

**Rules.**
1. **Earliest arrival first.** The goal is to reach the destination as early
   as possible, starting now. Arrival time is always the main criterion.
2. **Same trips → one connection.** If several connections ride exactly the
   same set of trips, only the best one is kept:
   1. earlier arrival;
   2. then less walking;
   3. then later departure.

   This drops pointless walks: walking to an earlier stop of the same bus
   (Haškova → Loosova for the N92 that also stops at Haškova), or getting off
   to walk to a stop that the same tram passes anyway (Slavíčkova → Čertova
   rokle instead of changing at Halasovo náměstí).
3. **Different trips → show each.** Every distinct set of trips gets its own
   best connection, and all of them are shown, even with the same arrival
   time. For example, 57 → S3 and 57 → tram 9, both leaving at 11:09 and
   arriving at 11:36, are both listed. So is 46 → tram 9 at 11:13, which uses
   the same tram trip but a different bus.

   This also holds when one of them arrives later: bus 46 at 11:13 → tram 9
   (arrives 11:26) and the same bus 46 at 11:13 → tram 5 (arrives 11:29) are
   both shown.
4. **Line numbers do not matter** for grouping; only the trips do. They are
   only used to look for alternatives: an alternative changes to a different
   line, not to a later vehicle of the same line. Waiting at the transfer for
   the next tram 9 is not offered, because a later first bus does the same.
5. **Sorting.** The final list is sorted by arrival at the destination; on
   equal arrival, the earlier departure comes first.

**Limits.** The list covers connections arriving no later than the last of
the ~6 main ones (see Planner below), departing within 3 hours from now, with
at most 6 rides each. That is up to 21 connections: 11 main plus 10
alternatives.

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
  - Connections reaching different stops of the destination at the same time
    are all returned (e.g. a train at platform 98 and a tram at platform 2).
  - When the same trip can be boarded in two ways in one round:
    - if both come from the same previous trip (or both from the origin), the
      one with less walking wins (same trips, rule 2);
    - otherwise the one leaving the origin earlier wins. The other one is a
      different set of trips and is found by a later planner iteration
      (rule 3).
  - A **transfer slack of 1 minute** is required on top of walking time. It
    applies only after a ride; the first boarding has no slack.
  - **Guaranteed connections.** `transfers.txt` also has ~100k trip-to-trip
    rows (`from_trip_id` → `to_trip_id`, with `min_transfer_time` and
    `max_waiting_time`, i.e. how long the next vehicle waits for a late one).
    When the trip you ride has such a row, the connecting trip can be boarded
    if it departs at least `min_transfer_time` after you arrive, with no slack
    and no walking minimum. Results show these as "Guaranteed connection ·
    waits up to N min" (or "Timed connection" when the wait is 0).
- **Planner** (`internal/planner/gtfs.go`)
  - Repeats the search, each time departing one minute after the earliest
    departure found in the previous search.
  - Once 6 distinct sets of trips are found, it keeps searching while new
    connections arrive no later than the latest one already found, so the
    tail of the list is cut by arrival, not by departure. Example: bus 57 at
    11:39 and bus 46 at 11:43 both arrive at 11:56, and both are shown.
  - Guardrails: at most 5 such extra connections (11 in total), 20 searches,
    and a 3-hour departure window.
  - **Alternatives** (rule 3). For every connection found, each ride except
    the first is replaced in turn: the search is repeated from the same
    departure with that ride's line excluded. Example: from 46 → tram 9 this
    finds 46 → tram 5. New connections are explored the same way, with their
    exclusions accumulated. Only connections arriving no later than the last
    main one are kept.
  - Guardrails for alternatives: at most 10 new connections and 30 extra
    searches.
  - Search time is truncated to the current minute, so a vehicle departing
    this minute is still offered.
  - Results are grouped by their set of trips as they come in, keeping the
    best of each group (rule 2). The count above is taken after grouping.
- **Sorting** (rule 5) happens in the web layer
  (`web.SortByEarliestArrival`).

A typical query takes 5–40 ms.

## Tests

`tests/routing` runs the planner on a pinned GTFS snapshot
(`tests/routing/testdata/gtfs-2026-11-20.zip`, valid 2026-11-20 … 2026-12-11)
and checks real-world scenarios. Every scenario also checks general invariants
(legs connect, no time travel, transfer slack, no duplicate trip sets).

```sh
go test ./tests/routing -v
```

Set `BRNO_GTFS` to run against a different feed.

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

**Approach path.** For a `live` leg, the matched station index tells where the
vehicle is on its trip. If it has not reached the boarding stop yet, the
server adds `approach`: the trip's stops from the vehicle's last stop to the
boarding stop, snapped to route geometry. The client draws it at 40 % opacity
beneath the route. It is cached per (leg, last stop) for the lifetime of the
connection.

The SSE endpoint recomputes the state at most every 300 ms when updates
arrive, and at least every 20 s (together with a keep-alive ping). It sends
an event only when the state changes.

## Route shapes

`internal/shapes.Store` loads the city's `transit_routes` GeoJSON: about 511
polylines, several variants per line. It caches the file for 7 days, using the
same download-validate-rename scheme as the GTFS feed (`internal/filecache`).

**Line names.** GTFS lines are mapped to layer names as follows:

| GTFS line                  | Layer name |
|----------------------------|------------|
| tram `9`                   | `T09`      |
| trolleybus `25`            | `TB25`     |
| bus `46`                   | `B46`      |
| night bus `N92`            | `B92`      |
| train `S3`                 | `V_S3`     |
| express bus `E56` and others | name as-is |

**Snapping.** A ride is snapped one stop pair at a time. A variant is used
for a pair when all of these hold:

- both stops lie within 80 m of the variant;
- the slice between them is no longer than 2.5 × the straight distance +
  300 m;
- the slice is taken in either direction: some variants are digitised
  backwards, so reversed slices are flipped.

The best-scoring slice wins. Pairs without a match stay straight. This
happens for diversions, where the layer shows the regular route, and for
lines missing from the layer.

Shapes are added server-side when `/map` is rendered (`Shape` in `mapLeg`),
so map links stay small.

## Project layout

```
cmd/server/            entry point, flags, wiring
internal/filecache/    download → validate → atomic replace, TTL checks
internal/gtfs/         GTFS zip parser and feed cache
internal/shapes/       route geometry store and stop-to-stop snapping
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
- Route geometry is missing for some lines and does not follow temporary
  diversions:
  - tram 7 is absent from the layer;
  - regional buses are named by licence number (`x_720260`) and cannot be
    mapped to IDS line numbers.

  Affected stop pairs fall back to straight lines.
- The approach path starts at the vehicle's last passed stop, not its exact
  position.
- Live markers jump every ~10 s. Smooth interpolation or dead-reckoning along
  the route is a planned next step.
- The vehicle for a trip that has not started yet cannot be predicted (no
  `block_id` in GTFS). It only shows up once it reaches the trip's first stop
  (up to 15 min before departure) or is running the trip.
- Optional filter not implemented: hide journeys with the same arrival but an
  earlier departure.
- The PWA splash background is dark in both themes (the manifest cannot
  follow the system theme).
