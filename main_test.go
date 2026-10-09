package main

import (
	"encoding/base64"
	"reflect"
	"testing"
)

func TestParsePelota(t *testing.T) {
	src := `<ul class="menu"><li class="NAT"><a href="#">
Liga de Naciones de la UEFA: Francia vs Bélgica
<span class="t">19:45</span></a>
<ul>
<li class="subitem1"><a href="/eventos.html?r=` + base64.StdEncoding.EncodeToString([]byte("https://x.test/a?stream=espn")) + `" target="_top">ESPN<span>Calidad 720p</span></a></li>
<li class="subitem1"><a href="/eventos.html?r=%%%" target="_top">Roto<span>Calidad 720p</span></a></li>
</ul></li></ul>`

	events := parsePelota(src)
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	e := events[0]
	if deref(e.League, "") != "Liga de Naciones de la UEFA" || e.Home != "Francia" || e.Away != "Bélgica" {
		t.Fatalf("bad matchup: %+v", e)
	}
	if deref(e.Time, "") != "15:45" { // UTC+1 → AR
		t.Fatalf("bad time: %v", deref(e.Time, ""))
	}
	q := "720p"
	want := []Option{{Source: "ESPN", Quality: &q, URL: "https://x.test/a?stream=espn", Embed: "https://x.test/a?stream=espn"}}
	if !reflect.DeepEqual(e.Options, want) {
		t.Fatalf("bad options: %+v", e.Options)
	}
}

func TestHelpers(t *testing.T) {
	if got := slug("Francia vs. Bélgica @ Ñandú_x"); got != "francia-vs-belgica-at-nandu-x" {
		t.Errorf("slug: %q", got)
	}
	if got := absolute("servers/x.php?id=1", "https://pelisjuanita.com/tv/"); got != "https://pelisjuanita.com/tv/servers/x.php?id=1" {
		t.Errorf("absolute: %q", got)
	}
	if got := unwrap("https://h/f.html?get=" + base64.StdEncoding.EncodeToString([]byte("https://s/a.m3u8"))); got != "https://s/a.m3u8" {
		t.Errorf("unwrap: %q", got)
	}
	if len(getChannels()) == 0 {
		t.Error("no channels")
	}
}

func TestParseJuanitaRelativeEmbed(t *testing.T) {
	var it juanitaItem
	it.Attributes.Desc = "Liga: A vs B"
	it.Attributes.Embeds.Data = append(it.Attributes.Embeds.Data, struct {
		Attributes struct {
			Name   string `json:"embed_name"`
			Iframe string `json:"embed_iframe"`
		} `json:"attributes"`
	}{})
	it.Attributes.Embeds.Data[0].Attributes.Iframe = "/embed/eventos.html?r=aHR0cHM6Ly90dmY5MC5jb20vMS5waHA/c3RyZWFtPWJlaW5zcG9ydGVz"
	e, ok := parseJuanitaItem(it)
	if !ok || len(e.Options) != 1 {
		t.Fatalf("bad event: %+v", e)
	}
	o := e.Options[0]
	if o.URL != "https://pelisjuanita.com/tv/embed/eventos.html?r=aHR0cHM6Ly90dmY5MC5jb20vMS5waHA/c3RyZWFtPWJlaW5zcG9ydGVz" || o.Embed != "https://tvf90.com/1.php?stream=beinsportes" {
		t.Fatalf("bad option: %+v", o)
	}
	if e.HomeLogo != nil {
		t.Fatalf("logo without id: %v", *e.HomeLogo)
	}
	it.Attributes.HomeID = "bcbj"
	if e, _ = parseJuanitaItem(it); e.HomeLogo == nil || *e.HomeLogo != "https://api.promiedos.com.ar/images/team/bcbj/1" || e.AwayLogo != nil {
		t.Fatalf("bad logos: %v %v", e.HomeLogo, e.AwayLogo)
	}
}

func TestExtinfLogo(t *testing.T) {
	logo := "https://x/l.png"
	if got := extinf("a", "G", "T", eventLogo(Event{AwayLogo: &logo})); got != `#EXTINF:-1 tvg-id="a" tvg-logo="https://x/l.png" group-title="G",T` {
		t.Fatal(got)
	}
	if got := extinf("a", "G", "T", ""); got != `#EXTINF:-1 tvg-id="a" group-title="G",T` {
		t.Fatal(got)
	}
}

func TestParseESPN(t *testing.T) {
	soccer := `{"events":[{"competitions":[{"id":"1","date":"2026-10-09T17:30Z","status":{"type":{"state":"in","shortDetail":"45'"}},
		"competitors":[{"homeAway":"home","score":"2","team":{"displayName":"Aldosivi","logo":"https://x/aldo.png"}},{"homeAway":"away","score":"1","team":{"displayName":"Sarmiento"}}]}]}]}`
	tennis := `{"events":[{"name":"China Open","groupings":[{"competitions":[{"id":"9","date":"2026-09-27T04:00Z","status":{"type":{"state":"post","shortDetail":"Final"}},
		"competitors":[{"homeAway":"away","winner":true,"athlete":{"displayName":"Arthur Gea","flag":{"href":"https://x/fra.png"}},"linescores":[{"value":6},{"value":6}]},
		{"homeAway":"home","athlete":{"displayName":"Te Rigele"},"linescores":[{"value":2},{"value":3}]}]}]}]}]}`

	s, err := parseESPN([]byte(soccer), "arg.1")
	want := Score{ID: "1", Date: "2026-10-09T14:30:00-03:00", State: "in", Detail: "45'", Home: Side{Name: "Aldosivi", Score: "2", Logo: "https://x/aldo.png"}, Away: Side{Name: "Sarmiento", Score: "1"}}
	if err != nil || len(s) != 1 || !reflect.DeepEqual(s[0], want) {
		t.Fatalf("soccer: %+v %v", s, err)
	}
	s, err = parseESPN([]byte(tennis), "atp")
	if err != nil || len(s) != 1 || s[0].Tournament != "China Open" || s[0].Away != (Side{Name: "Arthur Gea", Score: "6 6", Winner: true, Logo: "https://x/fra.png"}) || s[0].Home.Score != "2 3" {
		t.Fatalf("tennis: %+v %v", s, err)
	}

	mixed := `{"events":[{"name":"China Open","groupings":[
		{"grouping":{"slug":"mens-singles"},"competitions":[{"id":"1"}]},
		{"grouping":{"slug":"womens-singles"},"competitions":[{"id":"2"}]},
		{"grouping":{"slug":"mens-doubles"},"competitions":[{"id":"3"}]}]}]}`
	if s, _ = parseESPN([]byte(mixed), "atp"); len(s) != 2 || s[0].ID != "1" || s[1].Tournament != "China Open · dobles" {
		t.Fatalf("atp: %+v", s)
	}
	if s, _ = parseESPN([]byte(mixed), "wta"); len(s) != 1 || s[0].ID != "2" {
		t.Fatalf("wta: %+v", s)
	}

	f1 := `{"events":[{"name":"Bahrain GP","competitions":[{"id":"7","type":{"abbreviation":"Race"},"status":{"type":{"state":"post"}},"competitors":[
		{"order":3,"athlete":{"displayName":"C"}},{"order":1,"winner":true,"athlete":{"displayName":"A","flag":{"href":"https://x/ned.png"}}},
		{"order":4,"athlete":{"displayName":"D"}},{"order":2,"athlete":{"displayName":"B"}}]}]}]}`
	s, _ = parseESPN([]byte(f1), "f1")
	if len(s) != 1 || s[0].Session != "Race" || s[0].Tournament != "Bahrain GP" || len(s[0].Results) != 4 ||
		s[0].Results[0] != (Side{Name: "A", Winner: true, Logo: "https://x/ned.png"}) || s[0].Results[2].Name != "C" {
		t.Fatalf("f1: %+v", s)
	}
}

func TestParseSummary(t *testing.T) {
	body := `{"gameInfo":{"venue":{"fullName":"Guillermo Laza","address":{"city":"Buenos Aires"}}},
		"header":{"competitions":[{"competitors":[
			{"homeAway":"home","team":{"id":"1"},"linescores":[{"displayValue":"31"},{"displayValue":"21"}]},
			{"homeAway":"away","team":{"id":"2"},"linescores":[{"displayValue":"33"},{"displayValue":"23"}]}]}]},
		"boxscore":{"teams":[
			{"team":{"id":"2"},"statistics":[{"name":"possessionPct","displayValue":"40.8"},{"name":"turnovers","displayValue":"1"},{"name":"turnovers","displayValue":"9"}]},
			{"team":{"id":"1"},"statistics":[{"name":"possessionPct","displayValue":"59.2"},{"name":"turnovers","displayValue":"0"},{"name":"passPct","displayValue":"0.8"}]}]},
		"keyEvents":[
			{"type":{"type":"kickoff"},"clock":{"displayValue":"0'"}},
			{"type":{"type":"goal---free-kick"},"clock":{"displayValue":"3'"},"team":{"id":"1"},"participants":[{"athlete":{"displayName":"Milton Céliz"}}]},
			{"type":{"type":"yellow-card"},"clock":{"displayValue":"5'"},"team":{"id":"2"},"participants":[{"athlete":{"displayName":"Sansotre"}}]},
			{"type":{"type":"own-goal"},"clock":{"displayValue":"70'"},"team":{"id":"2"},"participants":[{"athlete":{"displayName":"Pérez"}}]}],
		"scoringPlays":[{"text":"TD run","clock":{"displayValue":"13:34"},"period":{"number":1},"team":{"id":"2"}}]}`
	d, err := parseSummary([]byte(body))
	want := Detail{
		Venue: "Guillermo Laza · Buenos Aires", HomePeriods: []string{"31", "21"}, AwayPeriods: []string{"33", "23"},
		Plays: []Play{{"3'", "home", "goal", "Milton Céliz"}, {"5'", "away", "yellow", "Sansotre"}, {"70'", "away", "goal", "Pérez (e/c)"}, {"Q1 13:34", "away", "score", "TD run"}},
		Stats: []Stat{{"Posesión %", "59.2", "40.8"}, {"Pérdidas", "0", "1"}},
	}
	if err != nil || !reflect.DeepEqual(d, want) {
		t.Fatalf("got %+v %v", d, err)
	}
}

func TestStreamedEvents(t *testing.T) {
	got := streamedEvents([]map[string]any{{"name": "C vs D", "category": "football", "date": "2026-10-09T18:30:00-03:00",
		"id": "m1", "sources": []any{map[string]any{"source": "alpha", "id": "c-d"}, map[string]any{"source": "", "id": "x"}}}},
		map[string]bool{"m1": true}, nil)
	e := got[0]
	if !e.Live || e.Popular || str(e.Raw["category"]) != "football" {
		t.Fatalf("bad flags/raw: %+v", e)
	}
	if e.Home != "C" || e.Away != "D" || deref(e.League, "") != "football" || deref(e.Date, "") != "2026-10-09" || deref(e.Time, "") != "18:30" {
		t.Fatalf("bad event: %+v", e)
	}
	if len(e.Options) != 1 || e.Options[0].URL != streamedAPI+"/stream/alpha/c-d" {
		t.Fatalf("bad options: %+v", e.Options)
	}
}

func TestStartsAt(t *testing.T) {
	d, h := "2026-10-09", "21:00"
	if got := startsAt(Event{Date: &d, Time: &h}); got == nil || got.UTC().Format("2006-01-02 15:04") != "2026-10-10 00:00" {
		t.Errorf("with date: %v", got)
	}
	if got := startsAt(Event{Time: &h}); got == nil || got.In(ar).Format("2006-01-02") != todayAR() {
		t.Errorf("without date: %v", got)
	}
	if startsAt(Event{Date: &d}) != nil {
		t.Error("without time should be nil")
	}
}
