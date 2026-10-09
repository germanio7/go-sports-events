package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"
)

// Grilla 24/7 de pelisjuanita.com/tv como JSON estático: el HTML vive tras Cloudflare. Sin Adultos.
//
//go:embed channels.json
var channelsJSON []byte

const tvBase = "https://pelisjuanita.com/tv/"

type Channel struct {
	Name     string
	Slug     string
	Category string
	Country  string
	Options  []Option
}

var getChannels = sync.OnceValue(func() []Channel {
	var raw []struct {
		Name      string
		Slug      string
		Categoria string
		Pais      string
		Options   []struct{ URL string }
	}
	if err := json.Unmarshal(channelsJSON, &raw); err != nil {
		panic(err) // embebido: si no parsea, es un bug de build
	}
	out := []Channel{}
	for _, c := range raw {
		name := strings.TrimSpace(c.Name)
		if name == "" || c.Categoria == "Adultos" {
			continue
		}
		var opts []Option
		for i, o := range c.Options {
			u := strings.TrimSpace(o.URL)
			if u == "" {
				continue
			}
			if !strings.HasPrefix(u, "//") {
				u = strings.TrimLeft(u, "/") // todo cuelga de /tv/, también "/servers/x.php"
			}
			opts = append(opts, Option{Source: fmt.Sprintf("S%d", i+1), URL: absolute(u, tvBase)})
		}
		if len(opts) == 0 {
			continue
		}
		cat := c.Categoria
		if cat == "" {
			cat = "Otros"
		}
		out = append(out, Channel{name, c.Slug, cat, c.Pais, opts})
	}
	return out
})

// ---- tvgarden (canales y radios abiertos de Argentina, datos de iptv-org) ----

var gardenAPI = strings.TrimRight(env("TVGARDEN_API_BASE", "https://tvgarden.world/api"), "/")

// getGarden: kind "tv" (.m3u8) o "radio" (mp3/aac); streams directos, sin solo-YouTube.
// En tv saltea los que ya están en la grilla 24/7.
func getGarden(kind string) []Channel {
	return cached("garden:"+kind, 6*time.Hour, func() ([]Channel, bool) {
		body, err := fetch(gardenAPI+"/"+kind+"/countries/ar.json", 2, time.Second, 20*time.Second, nil)
		var out []Channel
		if err == nil {
			var existing []Channel
			if kind == "tv" {
				existing = getChannels()
			}
			out, err = parseGarden(body, existing)
		}
		if err != nil {
			log.Printf("tvgarden %s fetch failed: %v", kind, err)
			return []Channel{}, false
		}
		return out, true
	})
}

func parseGarden(body []byte, existing []Channel) ([]Channel, error) {
	// sirven el .json gzipeado sin Content-Encoding: el cliente HTTP no lo descomprime solo.
	if bytes.HasPrefix(body, []byte{0x1f, 0x8b}) {
		r, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if body, err = io.ReadAll(r); err != nil {
			return nil, err
		}
	}
	var raw []struct {
		Name       string
		StreamURLs []string `json:"stream_urls"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, c := range existing {
		have[slug(c.Name)] = true
	}
	out := []Channel{}
	for _, c := range raw {
		name := strings.TrimSpace(c.Name)
		if name == "" || len(c.StreamURLs) == 0 || have[slug(name)] {
			continue
		}
		var opts []Option
		for i, u := range c.StreamURLs {
			opts = append(opts, Option{Source: fmt.Sprintf("S%d", i+1), URL: u})
		}
		out = append(out, Channel{name, slug(name), "Argentina", "ar", opts})
	}
	return out, nil
}
