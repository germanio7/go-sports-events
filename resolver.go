package main

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Sigue iframes hasta el player final y extrae el .m3u8 con regex.
// Sin resolvers por host: si un host cambia ofuscación, la opción cae al embed original.
// Los tokens duran ~5h → cache de 30m; fallos 1m. checkStreams saca los .m3u8 que mueren antes.

var (
	reIframe = regexp.MustCompile(`(?i)<iframe[^>]+src=["']([^"']+)["']`)
	reM3u8   = []*regexp.Regexp{
		regexp.MustCompile(`(?i)playbackURL\s*=\s*"([^"]+\.m3u8[^"]*)"`),
		regexp.MustCompile(`(?i)(?:source|file)\s*:\s*"([^"]+\.m3u8[^"]*)"`),
		regexp.MustCompile(`(?i)"(https?:[^"]+\.m3u8[^"]*)"`),
		regexp.MustCompile(`(?i)'(https?:[^']+\.m3u8[^']*)'`),
	}
)

// resolveMany: url original → .m3u8 (o la url tal cual si no resolvió).
func resolveMany(urls []string) map[string]string {
	targets, results, pending := map[string]string{}, map[string]string{}, map[string]string{}
	for _, u := range urls {
		if _, seen := targets[u]; seen {
			continue
		}
		t := unwrap(u)
		targets[u] = t
		if v, ok := cacheGet("resolve:" + t); ok {
			results[u] = v.(string)
		} else {
			pending[u] = t
		}
	}

	for depth := 3; len(pending) > 0 && depth > 0; {
		depth--
		bodies := fetchAll(pending)
		next := map[string]string{}
		for orig, target := range pending {
			body, ok := bodies[target]
			if !ok {
				results[orig] = target
				continue
			}
			if m := extractM3u8(body); m != "" {
				results[orig] = m
				continue
			}
			if m := reIframe.FindStringSubmatch(body); depth > 0 && m != nil {
				// algunos hosts escapan el src como \/6.php
				next[orig] = absolute(strings.ReplaceAll(m[1], `\`, ""), target)
				continue
			}
			results[orig] = target
		}
		pending = next
	}

	// fallos también, más corto: una opción muerta no frena cada sintonización del partido.
	for orig, final := range results {
		ttl := time.Minute
		if strings.Contains(final, ".m3u8") {
			ttl = 30 * time.Minute
		}
		cachePut("resolve:"+targets[orig], final, ttl)
	}
	return results
}

// fetchAll baja en paralelo los targets únicos; los que fallan no aparecen en el mapa.
func fetchAll(pending map[string]string) map[string]string {
	headers := map[string]string{"Referer": "https://pelisjuanita.com/"}
	bodies := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	seen := map[string]bool{}
	for _, u := range pending {
		if seen[u] {
			continue
		}
		seen[u] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b, err := fetchOnce(&http.Client{Timeout: 10 * time.Second}, u, headers); err == nil {
				mu.Lock()
				bodies[u] = string(b)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return bodies
}

// unwrap: ?r=<b64> o ?get=<url|b64> (pelisjuanita /tv) → url real.
func unwrap(u string) string {
	if d, ok := b64Param(u, "r"); ok && strings.HasPrefix(d, "http") {
		return d
	}
	if get := queryParam(u, "get"); strings.HasPrefix(get, "http") {
		return get
	}
	if d, ok := b64Param(u, "get"); ok && strings.HasPrefix(d, "http") {
		return d
	}
	return u
}

func extractM3u8(body string) string {
	for _, re := range reM3u8 {
		if m := re.FindStringSubmatch(body); m != nil && strings.HasPrefix(m[1], "http") {
			return html.UnescapeString(m[1])
		}
	}
	return ""
}

// absolute resuelve src relativo a base (directorio de base, no el origin).
func absolute(src, base string) string {
	b, err := url.Parse(base)
	if err != nil {
		return src
	}
	r, err := url.Parse(src)
	if err != nil {
		return src
	}
	if b.Scheme == "" {
		b.Scheme = "https"
	}
	return b.ResolveReference(r).String()
}

// checkStreams (cada 1m): re-resuelve lo vencido de juanita y después valida cada .m3u8 cacheado;
// los muertos quedan como fallo (1m) → /stream salta a la próxima opción. Hay orígenes que sirven
// un .m3u8 muerto, por eso se valida después de resolver y no antes.
// ponytail: un GET por opción viva por minuto (~100); solo juanita, la grilla 24/7 sigue en 30m.
func checkStreams() {
	var urls []string
	for _, e := range getJuanita() {
		urls = append(urls, optionURLs(e.Options)...)
	}
	resolveMany(urls)
	var wg sync.WaitGroup
	for _, u := range urls {
		key := "resolve:" + unwrap(u)
		v, ok := cacheGet(key)
		if !ok || !strings.Contains(v.(string), ".m3u8") {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !alive(v.(string)) {
				cachePut(key, "dead", time.Minute)
			}
		}()
	}
	wg.Wait()
}

// alive: el .m3u8 responde 2xx y es una playlist HLS.
func alive(m3u8 string) bool {
	b, err := fetchOnce(&http.Client{Timeout: 5 * time.Second}, m3u8, nil)
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(b)), "#EXTM3U")
}
