package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// Argentina y Bogotá no tienen DST: zonas fijas, sin depender de tzdata en la imagen.
var (
	ar     = time.FixedZone("ART", -3*3600)
	bogota = time.FixedZone("COT", -5*3600)
	utc1   = time.FixedZone("UTC+1", 3600)
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ---- cache ----

type entry struct {
	v   any
	exp time.Time
}

// ponytail: cache en memoria, se pierde al reiniciar y no barre claves vencidas que nadie relee; Redis/sweep si crece.
var mem = struct {
	sync.Mutex
	m map[string]entry
}{m: map[string]entry{}}

func cacheGet(key string) (any, bool) {
	mem.Lock()
	defer mem.Unlock()
	e, ok := mem.m[key]
	if !ok || time.Now().After(e.exp) {
		delete(mem.m, key)
		return nil, false
	}
	return e.v, true
}

func cachePut(key string, v any, ttl time.Duration) {
	mem.Lock()
	mem.m[key] = entry{v, time.Now().Add(ttl)}
	mem.Unlock()
}

// cached devuelve el valor cacheado o llama fn; fn decide si el resultado se guarda.
func cached[T any](key string, ttl time.Duration, fn func() (T, bool)) T {
	if v, ok := cacheGet(key); ok {
		return v.(T)
	}
	v, store := fn()
	if store {
		cachePut(key, v, ttl)
	}
	return v
}

// ---- http ----

// fetch hace GET con `attempts` intentos en total (como Http::retry de Laravel); non-2xx cuenta como fallo.
func fetch(u string, attempts int, delay, timeout time.Duration, headers map[string]string) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(delay)
		}
		var body []byte
		if body, err = fetchOnce(client, u, headers); err == nil {
			return body, nil
		}
	}
	return nil, err
}

func fetchOnce(client *http.Client, u string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, fmt.Errorf("GET %s: %s", u, res.Status)
	}
	return io.ReadAll(res.Body)
}

// ---- strings ----

var (
	reWS      = regexp.MustCompile(`\s+`)
	reNonSlug = regexp.MustCompile(`[^-a-z0-9\s]+`)
	reSlugSep = regexp.MustCompile(`[-\s]+`)
	reNL      = regexp.MustCompile(`[\r\n]+`)
	reQuality = regexp.MustCompile(`(?i)\b(\d{3,4}p)\b`)
)

func collapse(s string) string { return strings.TrimSpace(reWS.ReplaceAllString(s, " ")) }

func m3u(s string) string { return strings.TrimSpace(reNL.ReplaceAllString(s, " ")) }

// slug replica Str::slug de Laravel: ascii, minúsculas, '@'→'at', descarta símbolos, separador '-'.
func slug(s string) string {
	s, _, _ = transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn))), s)
	s = strings.ToLower(s)
	s = strings.NewReplacer("_", "-", "@", "-at-").Replace(s)
	s = reNonSlug.ReplaceAllString(s, "")
	return strings.Trim(reSlugSep.ReplaceAllString(s, "-"), "-")
}

func optionQuality(text string) *string {
	if m := reQuality.FindStringSubmatch(text); m != nil {
		return &m[1]
	}
	if strings.Contains(strings.ToUpper(text), "HD") {
		hd := "HD"
		return &hd
	}
	return nil
}

func b64(s string) (string, bool) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), true
		}
	}
	return "", false
}

func queryParam(rawURL, key string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Query().Get(key)
}

// b64Param decodifica el query param `key` de rawURL; false si falta o no es base64.
func b64Param(rawURL, key string) (string, bool) {
	v := queryParam(rawURL, key)
	if v == "" {
		return "", false
	}
	return b64(v)
}

func truthy(s string) bool {
	switch strings.ToLower(s) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

func deref(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}
