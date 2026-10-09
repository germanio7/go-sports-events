package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// Integración opcional con Jellyfin (JELLYFIN_URL + JELLYFIN_API_KEY en .env); sin config todo es no-op.
var (
	jellyfinURL = strings.TrimRight(env("JELLYFIN_URL", ""), "/")
	jellyfinKey = env("JELLYFIN_API_KEY", "")
	// instanceID va en cada respuesta (X-Instance): checkTuners lo usa para saber si un sintonizador apunta a este server.
	instanceID = rand.Text()
)

func jellyfinOn() bool { return jellyfinURL != "" && jellyfinKey != "" }

func jellyfin(method, path string, out any) error {
	req, err := http.NewRequest(method, jellyfinURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", `MediaBrowser Token="`+jellyfinKey+`"`)
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode > 299 {
		return fmt.Errorf("%s %s: %s", method, path, res.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// refreshGuide: corre "Actualizar la guía" para que Jellyfin vea partidos nuevos o borrados sin esperar la tarea programada.
func refreshGuide() {
	if !jellyfinOn() {
		return
	}
	var tasks []struct {
		ID  string `json:"Id"`
		Key string
	}
	err := jellyfin(http.MethodGet, "/ScheduledTasks?isHidden=false", &tasks)
	id := ""
	for _, t := range tasks {
		if t.Key == "RefreshGuide" {
			id = t.ID
		}
	}
	if err == nil && id == "" {
		err = fmt.Errorf("tarea RefreshGuide no encontrada")
	}
	if err == nil {
		err = jellyfin(http.MethodPost, "/ScheduledTasks/Running/"+id, nil)
	}
	if err != nil {
		log.Printf("jellyfin: %v", err)
		return
	}
	log.Printf("jellyfin: actualizando guía")
}

// checkWatched (cada 15s): valida solo las opciones del partido que alguien está mirando en Jellyfin,
// así un stream caído se detecta en ~15s y re-sintonizar va directo a otra opción.
func checkWatched() {
	if !jellyfinOn() {
		return
	}
	var sessions []struct {
		NowPlayingItem *struct{ Name string }
	}
	if err := jellyfin(http.MethodGet, "/Sessions?activeWithinSeconds=60", &sessions); err != nil {
		log.Printf("jellyfin sessions: %v", err)
		return
	}
	names := map[string]bool{}
	for _, s := range sessions {
		if s.NowPlayingItem != nil {
			names[s.NowPlayingItem.Name] = true
		}
	}
	if len(names) > 0 {
		validate(watchedURLs(getJuanita(), names))
	}
}

// watchedURLs: opciones cuyo canal (con o sin ?full=1) está en names.
func watchedURLs(events []Event, names map[string]bool) []string {
	var out []string
	for _, e := range events {
		title := matchTitle(e)
		for _, o := range e.Options {
			if names[title] || names[title+" — "+o.Source] {
				out = append(out, o.URL)
			}
		}
	}
	return out
}

// checkTuners (al arrancar): avisa si algún sintonizador de Jellyfin apunta a otro server o no responde.
func checkTuners() {
	if !jellyfinOn() {
		return
	}
	var cfg struct {
		TunerHosts []struct {
			URL string `json:"Url"`
		}
	}
	if err := jellyfin(http.MethodGet, "/System/Configuration/livetv", &cfg); err != nil {
		log.Printf("jellyfin tuners: %v", err)
		return
	}
	for _, t := range cfg.TunerHosts {
		if !strings.Contains(t.URL, "/api/juanita/") {
			continue
		}
		res, err := (&http.Client{Timeout: 10 * time.Second}).Get(t.URL)
		switch {
		case err != nil:
			log.Printf("jellyfin: ⚠ el sintonizador %s no responde desde acá: %v", t.URL, err)
		case res.Header.Get("X-Instance") != instanceID:
			log.Printf("jellyfin: ⚠ el sintonizador %s apunta a otro server, no a este", t.URL)
		default:
			log.Printf("jellyfin: sintonizador %s ok", t.URL)
		}
		if err == nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}
}
