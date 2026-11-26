package routing

import (
	"testing"

	"brnotransport/internal/model"
	"brnotransport/internal/planner"
)

func TestHaskovaToDetskaNemocnice_NoWalkToSameTram(t *testing.T) {
	r := plan(t, "Haškova", "Dětská nemocnice", "2026-11-21 22:29")

	r.expectNone("57 to Slavíčkova + walk to Čertova rokle for the tram 9 you could board at Halasovo náměstí",
		walks("Slavíčkova", "Čertova rokle"))
	r.expectSome("rides 57 to Halasovo náměstí and changes to tram 9 there without walking to another stop",
		and(noWalksBetweenStops, rides(
			ride{"57", "Haškova", "Halasovo náměstí"},
			ride{"9", "Halasovo náměstí", "Dětská nemocnice"},
		)))
}

func TestHaskovaToDetskaNemocnice_Bus46ThenTram5NotLost(t *testing.T) {
	r := plan(t, "Haškova", "Dětská nemocnice", "2026-11-22 10:45")

	r.expectSome("leaves Haškova on 46 at 10:58 and arrives on tram 5 at 11:09",
		func(j model.Journey) bool {
			if !boards("46", "Haškova", "10:58")(j) || hm(j.Arrival()) != "11:09" {
				return false
			}
			last := j.Legs[len(j.Legs)-1]
			return !last.IsWalk() && last.Line.Name == "5" && last.Guaranteed
		})
}

func TestHaskovaToDetskaNemocnice_Bus46At1143NotCutByLimit(t *testing.T) {
	r := plan(t, "Haškova", "Dětská nemocnice", "2026-11-22 11:06")

	r.expectSome("rides 57 at 11:39 arriving 11:56", func(j model.Journey) bool {
		return boards("57", "Haškova", "11:39")(j) && hm(j.Arrival()) == "11:56"
	})
	r.expectSome("rides 46 at 11:43 arriving 11:56 like the 57 at 11:39", func(j model.Journey) bool {
		return boards("46", "Haškova", "11:43")(j) && hm(j.Arrival()) == "11:56"
	})
}

func TestHaskovaToDetskaNemocnice_SameBusDifferentTramsAreSeparate(t *testing.T) {
	r := plan(t, "Haškova", "Dětská nemocnice", "2026-11-22 11:06", planner.Options{Alternatives: true})

	r.expectSome("rides 46 at 11:13 and changes to tram 9", and(boards("46", "Haškova", "11:13"), lastLine("9")))
	r.expectSome("rides the same 46 at 11:13 and changes to tram 5 instead", and(boards("46", "Haškova", "11:13"), lastLine("5")))
	r.expectNone("arrives more than 5 min after the 46 at 11:13 → tram 9 (11:26) it is an alternative to",
		func(j model.Journey) bool { return boards("46", "Haškova", "11:13")(j) && hm(j.Arrival()) > "11:31" })
}

func TestHaskovaToDetskaNemocnice_NoAlternativesByDefault(t *testing.T) {
	r := plan(t, "Haškova", "Dětská nemocnice", "2026-11-22 11:06")

	r.expectSome("rides 46 at 11:13 and changes to tram 9", and(boards("46", "Haškova", "11:13"), lastLine("9")))
	r.expectNone("rides the 46 at 11:13 to tram 5 (alternative, off by default)", and(boards("46", "Haškova", "11:13"), lastLine("5")))
}

func TestHaskovaToHlavniNadrazi_Bus57ThenTram9AlongsideTrain(t *testing.T) {
	r := plan(t, "Haškova", "Hlavní nádraží", "2026-11-22 10:53")

	r.expectSome("rides 57 at 11:09 and then train S3", and(boards("57", "Haškova", "11:09"), func(j model.Journey) bool {
		return j.Legs[len(j.Legs)-1].Line.Name == "S3"
	}))
	r.expectSome("rides 57 at 11:09 and changes to tram 9 at Halasovo náměstí",
		and(boards("57", "Haškova", "11:09"), noWalksBetweenStops, rides(
			ride{"57", "Haškova", "Halasovo náměstí"},
			ride{"9", "Halasovo náměstí", "Hlavní nádraží"},
		)))
}

func TestHaskovaToHlavniNadrazi_NoWalkToBoardSameNightBus(t *testing.T) {
	r := plan(t, "Haškova", "Hlavní nádraží", "2026-11-21 23:12")

	r.expectNone("walks from Haškova to Loosova to catch N92 that also stops at Haškova",
		walks("Haškova", "Loosova"))
	r.expectSome("boards the 00:40 N92 right at Haškova instead of walking to Loosova for it",
		and(noWalksBetweenStops, boards("N92", "Haškova", "00:40"), rides(ride{"N92", "Haškova", "Hlavní nádraží"})))
}

func TestCountReturnsMoreConnections(t *testing.T) {
	def := plan(t, "Haškova", "Hlavní nádraží", "2026-11-22 10:53")
	more := plan(t, "Haškova", "Hlavní nádraží", "2026-11-22 10:53", planner.Options{Count: 18})
	if len(more.journeys) < 18 {
		t.Errorf("Count 18 gave %d connections, want at least 18", len(more.journeys))
	}
	if len(more.journeys) <= len(def.journeys) {
		t.Errorf("Count 18 gave %d connections, default gave %d", len(more.journeys), len(def.journeys))
	}
}
