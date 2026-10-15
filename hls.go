package main

import (
	"crypto/sha256"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Relay HLS con prefetch. Hay CDNs (fubo18) que guardan 4 segmentos de 5s y tardan ~3s (a veces 12s) en servir
// cada uno; ffmpeg baja de a uno, se atrasa y salta ("skipping N segments ahead") → el reproductor se traba.
// El relay recarga el playlist upstream cada target/2, baja en paralelo cada segmento apenas aparece y publica
// en orden los ya bajados, guardando los últimos relayKeep. Jellyfin lee de acá en vez del CDN.
// Con una playlist maestra relaya una sola variante (bestVariant).
// ponytail: segmentos en memoria (~1MB c/u × relayKeep por partido mirado); solo playlists de media sin cifrar,
// KEY/MAP/VOD y maestras con EXT-X-MEDIA siguen con 302 al upstream.

const (
	relayKeep = 12               // ~60s con segmentos de 5s
	relayIdle = 30 * time.Second // sin pedidos de ningún cliente → se cierra
)

type segment struct {
	tags, url string
	data      []byte
	state     int // 0 bajando, 1 listo, 2 falló
}

type relay struct {
	mu      sync.Mutex
	src     string
	target  int
	pending map[int]*segment // seq upstream → segmento todavía sin publicar
	next    int              // próximo seq upstream a publicar
	last    int              // último seq upstream visto
	out     []*segment       // publicados, en orden
	base    int              // media sequence de out[0]
	seen    time.Time
	fresh   time.Time // último segmento nuevo en upstream, para loguear cortes de la fuente
}

var relays = struct {
	sync.Mutex
	m map[string]*relay
}{m: map[string]*relay{}}

// startRelay: id del relay de src (lo crea si no existe); false si src no es una playlist de media que sepa relayar.
func startRelay(src string) (string, bool) {
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(src)))[:16]
	relays.Lock()
	r := relays.m[id]
	relays.Unlock()
	if r != nil {
		return id, true
	}
	r = &relay{src: src, pending: map[int]*segment{}, seen: time.Now(), fresh: time.Now()}
	if !r.poll(true) { // fuera del lock global: el upstream puede tardar 10s
		return "", false
	}
	relays.Lock()
	defer relays.Unlock()
	if relays.m[id] == nil { // otro pedido pudo crearlo mientras tanto
		relays.m[id] = r
		go r.run(id)
	}
	return id, true
}

func (r *relay) run(id string) {
	for {
		r.mu.Lock()
		idle, wait := time.Since(r.seen) > relayIdle, time.Duration(r.target)*time.Second/2
		r.mu.Unlock()
		if idle {
			break
		}
		time.Sleep(wait)
		r.poll(false)
	}
	relays.Lock()
	delete(relays.m, id)
	relays.Unlock()
}

// poll recarga el playlist upstream y larga la descarga de cada segmento nuevo.
func (r *relay) poll(first bool) bool {
	b, err := fetchOnce(&http.Client{Timeout: 10 * time.Second}, r.src, nil)
	if err != nil {
		log.Printf("hls: %v", err)
		return false
	}
	seq, target, segs, ok := parseMedia(string(b), r.src)
	if v := bestVariant(string(b), r.src); !ok && first && v != "" && v != r.src {
		r.src = v // todavía no compartido: nadie más lo lee
		return r.poll(true)
	}
	if !ok {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.target = target
	if first {
		r.next = seq
	}
	for i, s := range segs {
		n := seq + i
		if n < r.next || r.pending[n] != nil {
			continue
		}
		r.pending[n] = s
		r.last = max(r.last, n)
		r.fresh = time.Now()
		go r.download(s)
	}
	if stall := time.Since(r.fresh); stall > time.Duration(2*target)*time.Second {
		log.Printf("hls %s: la fuente no publica segmentos nuevos hace %v", r.name(), stall.Round(time.Second))
	}
	return true
}

// name: src sin el query (los tokens no van al log).
func (r *relay) name() string {
	name, _, _ := strings.Cut(r.src, "?")
	return name
}

func (r *relay) download(s *segment) {
	t0 := time.Now()
	b, err := fetch(s.url, 2, time.Second, 20*time.Second, nil)
	r.mu.Lock()
	defer r.mu.Unlock()
	if took := time.Since(t0); err != nil || took > time.Duration(r.target)*time.Second {
		log.Printf("hls %s: segmento en %v (err=%v)", r.name(), took.Round(time.Millisecond), err != nil)
	}
	s.data, s.state = b, 1
	if err != nil {
		s.state = 2
	}
	r.publish()
}

// publish pasa a out los segmentos listos en orden: uno bajando frena la cola, uno caído o que no llegamos
// a ver en upstream se saltea.
func (r *relay) publish() {
	for r.next <= r.last {
		s := r.pending[r.next]
		if s != nil && s.state == 0 {
			break
		}
		if s != nil && s.state == 1 {
			r.out = append(r.out, s)
		}
		delete(r.pending, r.next)
		r.next++
	}
	if d := len(r.out) - relayKeep; d > 0 {
		r.out = r.out[d:]
		r.base += d
	}
}

// playlist propia; en el primer pedido espera a tener 3 segmentos (ffmpeg arranca 3 antes del final)
// mientras quede alguno bajando.
func (r *relay) playlist() string {
	deadline := time.Now().Add(20 * time.Second)
	r.mu.Lock()
	for len(r.out) < 3 && r.next <= r.last && time.Now().Before(deadline) {
		r.mu.Unlock()
		time.Sleep(200 * time.Millisecond)
		r.mu.Lock()
	}
	defer r.mu.Unlock()
	r.seen = time.Now()
	lines := []string{"#EXTM3U", "#EXT-X-VERSION:3", fmt.Sprintf("#EXT-X-TARGETDURATION:%d", r.target), fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d", r.base)}
	for i, s := range r.out {
		lines = append(lines, s.tags, fmt.Sprintf("%d.ts", r.base+i))
	}
	return strings.Join(lines, "\n") + "\n"
}

// hlsFile: /api/juanita/hls/{id}/live.m3u8 (playlist del relay) o /api/juanita/hls/{id}/{n}.ts (segmento en memoria).
func hlsFile(w http.ResponseWriter, req *http.Request) {
	relays.Lock()
	r := relays.m[req.PathValue("id")]
	relays.Unlock()
	if r == nil {
		http.NotFound(w, req)
		return
	}
	file := req.PathValue("file")
	if file == "live.m3u8" {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, r.playlist())
		return
	}
	n, err := strconv.Atoi(strings.TrimSuffix(file, ".ts"))
	var data []byte
	r.mu.Lock()
	r.seen = time.Now()
	if i := n - r.base; err == nil && i >= 0 && i < len(r.out) {
		data = r.out[i].data
	}
	r.mu.Unlock()
	if data == nil {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Write(data)
}

// parseMedia: media sequence, target duration y segmentos (con sus tags) de una playlist de media en vivo.
func parseMedia(body, base string) (seq, target int, segs []*segment, ok bool) {
	if !strings.HasPrefix(strings.TrimSpace(body), "#EXTM3U") {
		return 0, 0, nil, false
	}
	for _, tag := range []string{"#EXT-X-STREAM-INF", "#EXT-X-KEY", "#EXT-X-MAP", "#EXT-X-ENDLIST"} {
		if strings.Contains(body, tag) {
			return 0, 0, nil, false
		}
	}
	var tags []string
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(l)
		if v, found := strings.CutPrefix(l, "#EXT-X-MEDIA-SEQUENCE:"); found {
			seq, _ = strconv.Atoi(v)
		} else if v, found := strings.CutPrefix(l, "#EXT-X-TARGETDURATION:"); found {
			target, _ = strconv.Atoi(v)
		} else if strings.HasPrefix(l, "#EXTINF") || l == "#EXT-X-DISCONTINUITY" || strings.HasPrefix(l, "#EXT-X-PROGRAM-DATE-TIME") {
			tags = append(tags, l)
		} else if l != "" && !strings.HasPrefix(l, "#") {
			segs = append(segs, &segment{tags: strings.Join(tags, "\n"), url: absolute(l, base)})
			tags = nil
		}
	}
	return seq, target, segs, len(segs) > 0 && target > 0
}

var reBandwidth = regexp.MustCompile(`(?:^|,)BANDWIDTH=(\d+)`)

// bestVariant: la variante de más BANDWIDTH de una playlist maestra. Sin esto Jellyfin toma el video de la
// mejor y el audio de la primera, y ffmpeg baja dos variantes al ritmo de la más lenta (La 100: 0.23x).
// "" si no es maestra o trae audio/video aparte (EXT-X-MEDIA), que una sola variante no tendría.
func bestVariant(body, base string) string {
	if strings.Contains(body, "#EXT-X-MEDIA:") {
		return ""
	}
	best, bw := "", -1
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		attrs, ok := strings.CutPrefix(strings.TrimSpace(l), "#EXT-X-STREAM-INF:")
		if !ok || i+1 == len(lines) {
			continue
		}
		uri := strings.TrimSpace(lines[i+1])
		m := reBandwidth.FindStringSubmatch(attrs)
		if uri == "" || strings.HasPrefix(uri, "#") || m == nil {
			continue
		}
		if n, _ := strconv.Atoi(m[1]); n > bw {
			best, bw = absolute(uri, base), n
		}
	}
	return best
}

// redirectHLS: al relay si el upstream es una playlist que sabe relayar; si no, directo al upstream como antes.
func redirectHLS(w http.ResponseWriter, r *http.Request, m3u8 string) {
	if id, ok := startRelay(m3u8); ok {
		m3u8 = "/api/juanita/hls/" + id + "/live.m3u8"
	}
	http.Redirect(w, r, m3u8, http.StatusFound)
}
