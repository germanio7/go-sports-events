package main

import (
	"encoding/json"
	"log"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Event es la forma compartida por pelotalibre y pelisjuanita (la UI usa la misma).
type Event struct {
	League  *string  `json:"league"`
	Home    string   `json:"home"`
	Away    string   `json:"away"`
	Time    *string  `json:"time"`
	Date    *string  `json:"date,omitempty"`
	Channel *string  `json:"channel"`
	Quality *string  `json:"quality"`
	Options []Option `json:"options"`
	// solo juanita: logos de Promiedos por id de equipo
	HomeLogo *string `json:"homeLogo,omitempty"`
	AwayLogo *string `json:"awayLogo,omitempty"`
	// solo streamed, para /api/events desde la BD
	Raw     map[string]any `json:"-"`
	Live    bool           `json:"-"`
	Popular bool           `json:"-"`
}

type Option struct {
	Source  string  `json:"source"`
	Quality *string `json:"quality"`
	URL     string  `json:"url"`
	Embed   string  `json:"embed"`
}

var (
	reVs     = regexp.MustCompile(`(?i)\bvs\.?\b`)
	reSplit  = regexp.MustCompile(`(?i)\s+vs\.?\s+`)
	reLeague = regexp.MustCompile(`^([^:]+):\s*(.+)$`)
	reHHMM   = regexp.MustCompile(`^(\d{1,2}:\d{2})$`)
	reHour   = regexp.MustCompile(`^(\d{2}:\d{2})`)
	reDate   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)
	reHTTP   = regexp.MustCompile(`(?i)^https?://`)
)

// matchup parsea "Liga: Local vs Visitante"; fallback es la liga si no viene prefijo.
func matchup(title string, fallback *string) (league *string, home, away string, ok bool) {
	if title == "" || !reVs.MatchString(title) {
		return nil, "", "", false
	}
	league = fallback
	if m := reLeague.FindStringSubmatch(title); m != nil {
		if l := strings.TrimSpace(m[1]); l != "" {
			league = &l
		}
		title = strings.TrimSpace(m[2])
	}
	parts := reSplit.Split(title, 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return nil, "", "", false
	}
	return league, strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

func toAR(hhmm string, from *time.Location) *string {
	t, err := time.ParseInLocation("15:04", hhmm, from)
	if err != nil {
		return nil
	}
	s := t.In(ar).Format("15:04")
	return &s
}

// ---- pelotalibre.la (HTML) ----

var pelotaURL = env("PELOTALIBRE_AGENDA_URL", "https://pelotalibre.la/agenda.php")

func getPelota() []Event { return loadEvents("pelota") }

// fetchPelota: upstream directo, solo lo llama el sync.
func fetchPelota() []Event {
	body, err := fetch(pelotaURL, 2, 800*time.Millisecond, 15*time.Second, nil)
	if err != nil {
		log.Printf("pelota agenda fetch failed: %v", err)
		return []Event{}
	}
	return parsePelota(string(body))
}

// parsePelota: cada evento es `<li><a>Liga: A vs B<span class="t">HH:MM</span></a><ul><li><a href="/eventos.html?r=B64">…`.
// Hora upstream fija UTC+1 → AR.
func parsePelota(src string) []Event {
	out := []Event{}
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return out
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if isTag(n, "li") && hasTimeHead(n) {
			if e, ok := parsePelotaItem(n); ok {
				out = append(out, e)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

func hasTimeHead(li *html.Node) bool {
	for _, a := range children(li, "a") {
		for _, s := range children(a, "span") {
			if attr(s, "class") == "t" {
				return true
			}
		}
	}
	return false
}

func parsePelotaItem(li *html.Node) (Event, bool) {
	head := children(li, "a")[0]
	rawTime := strings.TrimSpace(text(find(head, func(n *html.Node) bool { return isTag(n, "span") && attr(n, "class") == "t" })))
	title := collapse(strings.ReplaceAll(text(head), rawTime, ""))

	league, home, away, ok := matchup(title, nil)
	if !ok {
		return Event{}, false
	}

	var t *string
	if m := reHHMM.FindStringSubmatch(rawTime); m != nil {
		t = toAR(m[1], utc1)
	}

	options := []Option{}
	for _, ul := range children(li, "ul") {
		for _, sub := range children(ul, "li") {
			for _, a := range children(sub, "a") {
				href, has := attrOK(a, "href")
				if !has {
					continue
				}
				u, ok := pelotaStreamURL(href)
				if !ok {
					continue
				}
				qualityText := strings.TrimSpace(text(find(a, func(n *html.Node) bool { return isTag(n, "span") })))
				name := collapse(strings.ReplaceAll(text(a), qualityText, ""))
				if name == "" {
					name = "OP"
				}
				options = append(options, Option{Source: name, Quality: optionQuality(qualityText + " " + name), URL: u, Embed: u})
			}
		}
	}

	return Event{League: league, Home: home, Away: away, Time: t, Options: options}, true
}

// pelotaStreamURL: `/eventos.html?r=BASE64` → url decodificada; hrefs http(s) absolutos pasan tal cual.
func pelotaStreamURL(href string) (string, bool) {
	u := href
	if d, ok := b64Param(href, "r"); ok {
		u = strings.TrimSpace(d)
	}
	return u, reHTTP.MatchString(u)
}

func isTag(n *html.Node, tag string) bool {
	return n != nil && n.Type == html.ElementNode && n.Data == tag
}

func children(n *html.Node, tag string) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if isTag(c, tag) {
			out = append(out, c)
		}
	}
	return out
}

func find(n *html.Node, pred func(*html.Node) bool) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if pred(c) {
			return c
		}
		if f := find(c, pred); f != nil {
			return f
		}
	}
	return nil
}

func text(n *html.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(text(c))
	}
	return b.String()
}

func attrOK(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func attr(n *html.Node, key string) string { v, _ := attrOK(n, key); return v }

// ---- pelisjuanita (JSON Strapi) ----

var juanitaURL = env("PELISJUANITA_AGENDA_URL", "")

type juanitaItem struct {
	Attributes struct {
		Desc    string `json:"diary_description"`
		Hour    string `json:"diary_hour"`
		Date    string `json:"date_diary"`
		HomeID  string `json:"idEquipo1"`
		AwayID  string `json:"idEquipo2"`
		Country struct {
			Data struct {
				Attributes struct {
					Name string `json:"name"`
				} `json:"attributes"`
			} `json:"data"`
		} `json:"country"`
		Embeds struct {
			Data []struct {
				Attributes struct {
					Name   string `json:"embed_name"`
					Iframe string `json:"embed_iframe"`
				} `json:"attributes"`
			} `json:"data"`
		} `json:"embeds"`
	} `json:"attributes"`
}

func getJuanita() []Event { return loadEvents("juanita") }

// fetchJuanita: upstream directo, solo lo llama el sync.
func fetchJuanita() []Event {
	if juanitaURL == "" {
		return []Event{}
	}
	body, err := fetch(juanitaURL, 2, 800*time.Millisecond, 15*time.Second, nil)
	var payload struct{ Data []juanitaItem }
	if err == nil {
		err = json.Unmarshal(body, &payload)
	}
	if err != nil {
		log.Printf("juanita agenda fetch failed: %v", err)
		return []Event{}
	}
	events := []Event{}
	for _, it := range payload.Data {
		if e, ok := parseJuanitaItem(it); ok {
			events = append(events, e)
		}
	}
	return events
}

func parseJuanitaItem(it juanitaItem) (Event, bool) {
	a := it.Attributes
	var country *string
	if c := a.Country.Data.Attributes.Name; c != "" {
		country = &c
	}
	league, home, away, ok := matchup(collapse(a.Desc), country)
	if !ok {
		return Event{}, false
	}

	// Upstream guarda hora UTC-5 fija → AR (UTC-3).
	var t, date *string
	if m := reHour.FindStringSubmatch(a.Hour); m != nil {
		t = toAR(m[1], bogota)
	}
	if d := reDate.FindString(a.Date); d != "" {
		date = &d
	}

	options := []Option{}
	for _, e := range a.Embeds.Data {
		name, u := strings.TrimSpace(e.Attributes.Name), e.Attributes.Iframe
		if u == "" {
			continue
		}
		src := name
		if src == "" {
			src = "OP"
		}
		// iframes vienen relativos a la app /tv ("/embed/eventos.html?r=…"); en la raíz da 404.
		if strings.HasPrefix(u, "/") {
			u = strings.TrimSuffix(tvBase, "/") + u
		}
		embed := u
		if d, ok := b64Param(u, "r"); ok {
			embed = d
		}
		options = append(options, Option{Source: src, Quality: optionQuality(name), URL: u, Embed: embed})
	}

	return Event{League: league, Home: home, Away: away, Time: t, Date: date, Options: options,
		HomeLogo: promiedosLogo(a.HomeID), AwayLogo: promiedosLogo(a.AwayID)}, true
}

// promiedosLogo: idEquipo de juanita = id de equipo de Promiedos (PNG público, CORS *).
func promiedosLogo(id string) *string {
	if id == "" {
		return nil
	}
	u := "https://api.promiedos.com.ar/images/team/" + url.PathEscape(id) + "/1"
	return &u
}
