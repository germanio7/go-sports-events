package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	streamedAPI = strings.TrimRight(env("STREAMED_API_BASE", "https://streamed.pk/api"), "/")
	streamedImg = strings.TrimRight(env("STREAMED_IMG_BASE", "https://streamed.pk"), "/")
)

func streamedJSON(u string) []map[string]any {
	body, err := fetch(u, 5, 1500*time.Millisecond, 30*time.Second, nil)
	var out []map[string]any
	if err == nil {
		err = json.Unmarshal(body, &out)
	}
	if err != nil {
		log.Printf("streamed fetch failed: %v", err)
		return nil
	}
	return out
}

func getSports() []map[string]string {
	return cached("streamed:sports", time.Hour, func() ([]map[string]string, bool) {
		out := []map[string]string{}
		for _, s := range streamedJSON(streamedAPI + "/sports") {
			id, name := str(s["id"]), str(s["name"])
			if name == "" {
				name = id
			}
			if id != "" {
				out = append(out, map[string]string{"id": id, "name": name})
			}
		}
		return out, len(out) > 0
	})
}

// getEvents: live+popular no filtra por categoría (el pool upstream es chico); live solo sí.
func getEvents(sport string, live, popular bool) []map[string]any {
	if sport == "" {
		sport = "football"
	}
	seg, key := "", sport
	if popular {
		seg = "popular"
	}
	if live {
		key = "live"
	}
	return cached("streamed:events:"+key+":"+seg, time.Minute, func() ([]map[string]any, bool) {
		u := streamedAPI + "/matches/" + url.PathEscape(sport) + "/" + seg
		if live {
			u = streamedAPI + "/matches/live/" + seg
		}
		matches := streamedJSON(u)
		if live && !popular {
			kept := matches[:0]
			for _, m := range matches {
				if str(m["category"]) == sport {
					kept = append(kept, m)
				}
			}
			matches = kept
		}
		sort.SliceStable(matches, func(i, j int) bool { return unixOrMax(matches[i]["date"]) < unixOrMax(matches[j]["date"]) })

		out := make([]map[string]any, 0, len(matches))
		for _, m := range matches {
			image := "/notfound.jpg"
			if m["poster"] != nil {
				// relativa: el navegador la pide a /api/images (proxy abajo), no a streamed.pk que el DNS del ISP no resuelve.
				image = str(m["poster"])
			}
			sources := m["sources"]
			if sources == nil {
				sources = []any{}
			}
			var date any
			if ts, ok := unix(m["date"]); ok {
				date = time.Unix(ts, 0).In(ar).Format("2006-01-02T15:04:05-07:00")
			}
			out = append(out, map[string]any{
				"id": m["id"], "name": m["title"], "image": image,
				"date": date, "category": m["category"], "sources": sources,
			})
		}
		return out, matches != nil
	})
}

// getStream solo cachea resultados no vacíos: el próximo click reintenta upstream.
func getStream(source, id string) []map[string]any {
	return cached("streamed:stream:"+source+":"+id, time.Minute, func() ([]map[string]any, bool) {
		streams := streamedJSON(streamedAPI + "/stream/" + url.PathEscape(source) + "/" + url.PathEscape(id))
		if streams == nil {
			streams = []map[string]any{}
		}
		return streams, len(streams) > 0
	})
}

// unix acepta ms o s (numérico o string) y fechas RFC3339.
func unix(v any) (int64, bool) {
	var n float64
	switch d := v.(type) {
	case float64:
		n = d
	case string:
		f, err := strconv.ParseFloat(d, 64)
		if err != nil {
			t, err := time.Parse(time.RFC3339, d)
			return t.Unix(), err == nil
		}
		n = f
	default:
		return 0, false
	}
	ts := int64(n)
	if ts > 10_000_000_000 {
		ts /= 1000
	}
	return ts, true
}

func unixOrMax(v any) int64 {
	if ts, ok := unix(v); ok {
		return ts
	}
	return math.MaxInt64
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
