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
