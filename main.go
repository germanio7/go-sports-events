package main

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		writeJSON(w, getEvents(q.Get("sport"), truthy(q.Get("live")), truthy(q.Get("popular"))))
	})
	mux.HandleFunc("GET /api/stream", func(w http.ResponseWriter, r *http.Request) {
		source, id := r.URL.Query().Get("source"), r.URL.Query().Get("id")
		if source == "" || id == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			writeJSON(w, map[string]string{"message": "source and id are required"})
			return
		}
		writeJSON(w, getStream(source, id))
	})
	mux.HandleFunc("GET /api/sports", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, getSports()) })
	mux.HandleFunc("GET /api/pelota/agenda", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"source": "futbollibrehd.me/api/agenda", "events": getPelota()})
	})
	mux.HandleFunc("GET /api/juanita/agenda", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"source": "pelisjuanita.com/tv/api-agenda.php", "events": getJuanita()})
	})
	mux.HandleFunc("GET /api/juanita/playlist.m3u", playlist)
	mux.HandleFunc("GET /api/juanita/epg.xml", epg)
	mux.HandleFunc("GET /api/juanita/tv/playlist.m3u", tvPlaylist)
	mux.HandleFunc("GET /api/juanita/stream", stream)
	mux.HandleFunc("GET /api/juanita/tv/stream", stream)

	addr := ":" + env("PORT", "8080")
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func todayAR() string { return time.Now().In(ar).Format("2006-01-02") }

func baseURL(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeM3U(w http.ResponseWriter, filename string, lines []string) {
	w.Header().Set("Content-Type", "audio/x-mpegurl")
	w.Header().Set("Content-Disposition", `inline; filename="`+filename+`"`)
	fmt.Fprint(w, strings.Join(lines, "\n")+"\n")
}

func streamLink(base, path string, urls ...string) string {
	return base + path + "?" + url.Values{"u": urls}.Encode()
}

func optionURLs(opts []Option) []string {
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.URL
	}
	return out
}

func extinf(id, group, title string) string {
	return fmt.Sprintf(`#EXTINF:-1 tvg-id="%s" group-title="%s",%s`, id, m3u(group), m3u(title))
}

// playlist (Jellyfin): un canal por partido con URL proxy estable; resuelve al .m3u8 al sintonizar.
// ?full=1 una entrada por opción; ?resolve=1 .m3u8 inline (tokens expiran).
func playlist(w http.ResponseWriter, r *http.Request) {
	events := getJuanita()
	if truthy(r.URL.Query().Get("resolve")) {
		directPlaylist(w, events)
		return
	}
	full, base := truthy(r.URL.Query().Get("full")), baseURL(r)
	lines := []string{"#EXTM3U"}
	for _, e := range events {
		if len(e.Options) == 0 {
			continue
		}
		league, id := deref(e.League, "Otros"), slug(e.Home+" vs "+e.Away)
		title := fmt.Sprintf("%s vs %s (%s)", e.Home, e.Away, deref(e.Time, ""))
		if !full {
			lines = append(lines, extinf(id, league, title), streamLink(base, "/api/juanita/stream", optionURLs(e.Options)...))
			continue
		}
		for _, o := range e.Options {
			lines = append(lines, extinf(id+"-"+slug(o.Source), league, title+" — "+o.Source), streamLink(base, "/api/juanita/stream", o.URL))
		}
	}
	writeM3U(w, "juanita.m3u", lines)
}

func directPlaylist(w http.ResponseWriter, events []Event) {
	var urls []string
	for _, e := range events {
		urls = append(urls, optionURLs(e.Options)...)
	}
	resolved := resolveMany(urls)
	lines := []string{"#EXTM3U"}
	for _, e := range events {
		for _, o := range e.Options {
			u := resolved[o.URL]
			title := fmt.Sprintf("%s vs %s (%s) — %s", e.Home, e.Away, deref(e.Time, ""), o.Source)
			lines = append(lines, fmt.Sprintf(`#EXTINF:-1 group-title="%s",%s`, m3u(deref(e.League, "Otros")), m3u(title)), u)
		}
	}
	writeM3U(w, "juanita.m3u", lines)
}

// epg: XMLTV mínimo, 2h fijas por partido; ids = tvg-id de la playlist.
func epg(w http.ResponseWriter, r *http.Request) {
	full := truthy(r.URL.Query().Get("full"))
	out := []string{`<?xml version="1.0" encoding="UTF-8"?>`, "<tv>"}
	add := func(id, name, start, stop string) {
		name = html.EscapeString(name)
		out = append(out,
			fmt.Sprintf(`  <channel id="%s"><display-name>%s</display-name></channel>`, id, name),
			fmt.Sprintf(`  <programme start="%s" stop="%s" channel="%s"><title>%s</title></programme>`, start, stop, id, name))
	}
	for _, e := range getJuanita() {
		if len(e.Options) == 0 || e.Time == nil {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 15:04", deref(e.Date, todayAR())+" "+*e.Time, ar)
		if err != nil {
			continue
		}
		const layout = "20060102150405 -0700"
		start, stop := t.Format(layout), t.Add(2*time.Hour).Format(layout)
		id, name := slug(e.Home+" vs "+e.Away), e.Home+" vs "+e.Away
		if !full {
			add(id, name, start, stop)
			continue
		}
		for _, o := range e.Options {
			add(id+"-"+slug(o.Source), name+" — "+o.Source, start, stop)
		}
	}
	out = append(out, "</tv>")
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprint(w, strings.Join(out, "\n")+"\n")
}

// tvPlaylist: grilla 24/7 (sin EPG), mismo patrón que playlist.
func tvPlaylist(w http.ResponseWriter, r *http.Request) {
	full, base := truthy(r.URL.Query().Get("full")), baseURL(r)
	lines := []string{"#EXTM3U"}
	for _, c := range getChannels() {
		id := slug(c.Name)
		if !full {
			lines = append(lines, extinf(id, c.Category, c.Name), streamLink(base, "/api/juanita/tv/stream", optionURLs(c.Options)...))
			continue
		}
		for _, o := range c.Options {
			lines = append(lines, extinf(id+"-"+slug(o.Source), c.Category, c.Name+" — "+o.Source), streamLink(base, "/api/juanita/tv/stream", o.URL))
		}
	}
	writeM3U(w, "juanita-tv.m3u", lines)
}

// stream: 302 al primer .m3u8 vivo, en el orden de upstream (la primera suele ser HD).
// Acepta `u=` repetido y `u[0]=` (playlists generadas por la versión Laravel).
func stream(w http.ResponseWriter, r *http.Request) {
	var embeds []string
	for _, kv := range strings.Split(r.URL.RawQuery, "&") {
		k, v, _ := strings.Cut(kv, "=")
		k, _ = url.QueryUnescape(k)
		v, _ = url.QueryUnescape(v)
		if (k == "u" || strings.HasPrefix(k, "u[")) && (strings.HasPrefix(v, "http") || strings.HasPrefix(v, "/")) {
			embeds = append(embeds, v)
		}
	}
	if len(embeds) == 0 {
		http.Error(w, "missing u", http.StatusBadRequest)
		return
	}
	resolved := resolveMany(embeds)
	for _, u := range embeds {
		if t := resolved[u]; strings.Contains(t, ".m3u8") {
			http.Redirect(w, r, t, http.StatusFound)
			return
		}
	}
	http.Error(w, "no live stream", http.StatusBadGateway)
}
