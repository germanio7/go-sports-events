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
// Los tokens expiran → cache corto, y solo de éxitos.

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

	for orig, final := range results {
		if strings.Contains(final, ".m3u8") {
			cachePut("resolve:"+targets[orig], final, 2*time.Minute)
		}
	}
	return results
}

// fetchAll baja en paralelo los targets únicos; los que fallan no aparecen en el mapa.
func fetchAll(pending map[string]string) map[string]string {
	headers := map[string]string{"User-Agent": userAgent, "Referer": "https://pelisjuanita.com/"}
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
