package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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
