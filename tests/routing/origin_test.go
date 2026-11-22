package routing

import (
	"testing"

	"brnotransport/internal/model"
	"brnotransport/internal/planner"
)

const nearHaskovaLat, nearHaskovaLon = 49.2334, 16.6230

func TestOrigin_StartsWithWalkFromLocation(t *testing.T) {
	r := planFrom(t, nearHaskovaLat, nearHaskovaLon, "Dětská nemocnice", "2026-11-22 10:45")

	for i, j := range r.journeys {
		first := j.Legs[0]
		if !first.IsWalk() || first.From.Name != planner.OriginName {
			t.Errorf("journey #%d does not start with a walk from %s: %s", i+1, planner.OriginName, describeLeg(first))
			continue
		}
		if first.DistanceMeters > 500 {
			t.Errorf("journey #%d walks %d m from the location, more than the 400 m radius allows", i+1, first.DistanceMeters)
		}
	}
	r.expectSome("walks to Haškova and boards 46 at 10:58", and(
		walks(planner.OriginName, "Haškova"),
		boards("46", "Haškova", "10:58"),
	))
	r.expectSome("boards the 10:59 57 at Haškova", boards("57", "Haškova", "10:59"))
	r.expectSome("boards the same 57 one stop earlier at Loosova, as a separate option", boards("57", "Loosova", "11:00"))
	r.expectNone("rides 57 from Loosova to Haškova just to change to 46, which is reachable on foot",
		rides(ride{"57", "Loosova", "Haškova"}, ride{"46", "Haškova", "Štefánikova čtvrť"}, ride{"5", "Štefánikova čtvrť", "Dětská nemocnice"}))
}

func TestStation_NoInitialWalkToAnotherStation(t *testing.T) {
	r := plan(t, "Jugoslávská", "Hlavní nádraží", "2026-11-22 14:55")
	r.expectNone("walks from Jugoslávská to Trávníčkova first", walks("Jugoslávská", "Trávníčkova"))
}

func TestOrigin_NoStopWithinRadius(t *testing.T) {
	r := planRaw(t, "", "Dětská nemocnice", "2026-11-22 10:45", planner.Options{Origin: &model.Point{Lat: 50.0, Lon: 14.0}})
	if len(r.journeys) != 0 {
		t.Errorf("expected no journeys from a location without stops within 400 m, got %d", len(r.journeys))
	}
}
