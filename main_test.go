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
	if len(s) != 1 || s[0].Session != "Race" || s[0].Tournament != "Bahrain GP" || len(s[0].Podium) != 3 ||
		s[0].Podium[0] != (Side{Name: "A", Winner: true, Logo: "https://x/ned.png"}) || s[0].Podium[2].Name != "C" {
		t.Fatalf("f1: %+v", s)
	}
}
