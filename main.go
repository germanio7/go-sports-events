package main

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

func main() {
	initDB()
	every(mustDuration(env("SYNC_EVERY", "2m")), syncEvents)
	every(time.Hour, pruneEvents)
	every(5*time.Minute, warmChannels)
	every(time.Minute, checkStreams)

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
	// posters de streamed (/api/images/proxy/...) vía este server, que sí resuelve streamed.pk.
	mux.HandleFunc("GET /api/images/", func(w http.ResponseWriter, r *http.Request) {
		b := cachedBytes("img:"+r.URL.Path, 24*time.Hour, streamedImg+r.URL.Path)
		if b == nil {
			http.Error(w, "image unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(b))
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(b)
	})
	mux.HandleFunc("GET /api/sports", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, getSports()) })
	mux.HandleFunc("GET /api/scores", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sport, league := q.Get("sport"), q.Get("league")
		if sport == "" || league == "" {
			sport, league = "soccer", "arg.1"
		}
		writeJSON(w, map[string]any{"sport": sport, "league": league, "scores": getScores(sport, league)})
	})
	mux.HandleFunc("GET /api/scores/{id}", func(w http.ResponseWriter, r *http.Request) {
		q, id := r.URL.Query(), r.PathValue("id")
		if q.Get("sport") == "" || q.Get("league") == "" || !reDigits.MatchString(id) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			writeJSON(w, map[string]string{"message": "sport, league and numeric id are required"})
			return
		}
		d, ok := getDetail(q.Get("sport"), q.Get("league"), id)
		if !ok {
			http.Error(w, "detail unavailable", http.StatusBadGateway)
			return
		}
		writeJSON(w, d)
	})
	mux.HandleFunc("GET /api/pelota/agenda", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"source": "pelotalibre.la/agenda.php", "events": getPelota()})
	})
	mux.HandleFunc("GET /api/juanita/agenda", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"source": "pelisjuanita.com/tv/api-agenda.php", "events": getJuanita()})
	})
	mux.HandleFunc("GET /api/juanita/playlist.m3u", playlist)
	mux.HandleFunc("GET /api/juanita/epg.xml", epg)
	mux.HandleFunc("GET /api/juanita/tv/playlist.m3u", tvPlaylist)
	// logos de la grilla 24/7: pelisjuanita los sirve tras Cloudflare, Jellyfin no llega directo.
	mux.HandleFunc("GET /api/juanita/tv/logos/{file}", func(w http.ResponseWriter, r *http.Request) {
		file := r.PathValue("file")
		if !reLogoFile.MatchString(file) {
			http.NotFound(w, r)
			return
		}
		b := cachedBytes("logo:"+file, 7*24*time.Hour, tvBase+"logos/"+file)
		if b == nil {
			http.Error(w, "logo unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(b))
		w.Header().Set("Cache-Control", "public, max-age=604800")
		w.Write(b)
	})
	mux.HandleFunc("GET /api/juanita/stream", stream)
	mux.HandleFunc("GET /api/juanita/tv/stream", stream)

	addr := ":" + env("PORT", "8080")
	log.Printf("listening on %s", addr)
	// ReadHeaderTimeout: el puerto está expuesto, corta clientes que no terminan los headers (slowloris).
	// Sin WriteTimeout: /stream puede tardar en resolver.
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// cachedBytes: imágenes de upstream cacheadas en memoria (solo éxitos); nil si falla.
// ponytail: sin límite de tamaño, ~13MB/día de posters; LRU si la memoria molesta.
func cachedBytes(key string, ttl time.Duration, u string) []byte {
	return cached(key, ttl, func() ([]byte, bool) {
		b, err := fetchOnce(&http.Client{Timeout: 10 * time.Second}, u, nil)
		return b, err == nil
	})
}

var reDigits = regexp.MustCompile(`^[0-9]+$`)

var reLogoFile = regexp.MustCompile(`^[a-z0-9-]+\.png$`)

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

func extinf(id, group, title, logo string) string {
	return fmt.Sprintf(`#EXTINF:-1 tvg-id="%s"%s group-title="%s",%s`, id, logoAttr(logo), m3u(group), m3u(title))
}

func logoAttr(logo string) string {
	if logo == "" {
		return ""
	}
	return fmt.Sprintf(` tvg-logo="%s"`, logo)
}

// eventLogo: un canal = un logo; local, o visitante si falta.
func eventLogo(e Event) string {
	return deref(e.HomeLogo, deref(e.AwayLogo, ""))
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
			lines = append(lines, extinf(id, league, title, eventLogo(e)), streamLink(base, "/api/juanita/stream", optionURLs(e.Options)...))
			continue
		}
		for _, o := range e.Options {
			lines = append(lines, extinf(id+"-"+slug(o.Source), league, title+" — "+o.Source, eventLogo(e)), streamLink(base, "/api/juanita/stream", o.URL))
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
			lines = append(lines, fmt.Sprintf(`#EXTINF:-1%s group-title="%s",%s`, logoAttr(eventLogo(e)), m3u(deref(e.League, "Otros")), m3u(title)), u)
		}
	}
	writeM3U(w, "juanita.m3u", lines)
}

// epg: XMLTV mínimo, 2h fijas por partido + "Previa" desde la hora actual; ids = tvg-id de la playlist.
// category Sports → Jellyfin lo lista en Deportes.
func epg(w http.ResponseWriter, r *http.Request) {
	full := truthy(r.URL.Query().Get("full"))
	out := []string{`<?xml version="1.0" encoding="UTF-8"?>`, "<tv>"}
	const layout = "20060102150405 -0700"
	now := time.Now().Truncate(time.Hour).In(ar)
	add := func(id, name, desc string, t time.Time, logo string) {
		name, desc, icon := html.EscapeString(name), html.EscapeString(desc), ""
		if logo != "" {
			icon = fmt.Sprintf(`<icon src="%s"/>`, html.EscapeString(logo))
		}
		out = append(out, fmt.Sprintf(`  <channel id="%s"><display-name>%s</display-name>%s</channel>`, id, name, icon))
		if now.Before(t) {
			out = append(out, fmt.Sprintf(`  <programme start="%s" stop="%s" channel="%s"><title>Previa: %s</title><desc>%s</desc>%s</programme>`,
				now.Format(layout), t.Format(layout), id, name, desc, icon))
		}
		out = append(out, fmt.Sprintf(`  <programme start="%s" stop="%s" channel="%s"><title>%s</title><desc>%s</desc><category lang="en">Sports</category>%s</programme>`,
			t.Format(layout), t.Add(2*time.Hour).Format(layout), id, name, desc, icon))
	}
	for _, e := range getJuanita() {
		t := startsAt(e)
		if len(e.Options) == 0 || t == nil {
			continue
		}
		id, name := slug(e.Home+" vs "+e.Away), e.Home+" vs "+e.Away
		sources := make([]string, len(e.Options))
		for i, o := range e.Options {
			sources[i] = o.Source
		}
		desc := deref(e.League, "Otros") + " · " + strings.Join(sources, ", ")
		if !full {
			add(id, name, desc, *t, eventLogo(e))
			continue
		}
		for _, o := range e.Options {
			add(id+"-"+slug(o.Source), name+" — "+o.Source, desc, *t, eventLogo(e))
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
		id, logo := slug(c.Name), base+"/api/juanita/tv/logos/"+c.Slug+".png"
		if !full {
			lines = append(lines, extinf(id, c.Category, c.Name, logo), streamLink(base, "/api/juanita/tv/stream", optionURLs(c.Options)...))
			continue
		}
		for _, o := range c.Options {
			lines = append(lines, extinf(id+"-"+slug(o.Source), c.Category, c.Name+" — "+o.Source, logo), streamLink(base, "/api/juanita/tv/stream", o.URL))
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
	if !slices.ContainsFunc(embeds, func(u string) bool { return strings.Contains(resolved[u], ".m3u8") }) {
		// todo falló en caché: el partido pudo arrancar recién → reintenta upstream ya en vez de 502.
		for _, u := range embeds {
			cacheDel("resolve:" + unwrap(u))
		}
		resolved = resolveMany(embeds)
	}
	for _, u := range embeds {
		if t := resolved[u]; strings.Contains(t, ".m3u8") {
			http.Redirect(w, r, t, http.StatusFound)
			return
		}
	}
	http.Error(w, "no live stream", http.StatusBadGateway)
}
