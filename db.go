package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// events 1 ───< options: Event/Option de agenda.go tal cual.
const schema = `
CREATE TABLE IF NOT EXISTS events (
	id         bigserial   PRIMARY KEY,
	provider   text        NOT NULL,
	day        date        NOT NULL,
	starts_at  timestamptz,
	league     text,
	home       text        NOT NULL,
	away       text        NOT NULL,
	home_logo  text,
	away_logo  text,
	first_seen timestamptz NOT NULL DEFAULT now(),
	last_seen  timestamptz NOT NULL DEFAULT now(),
	UNIQUE (provider, day, home, away)
);
CREATE TABLE IF NOT EXISTS options (
	id       bigserial PRIMARY KEY,
	event_id bigint    NOT NULL REFERENCES events ON DELETE CASCADE,
	source   text      NOT NULL,
	quality  text,
	url      text      NOT NULL,
	embed    text      NOT NULL
);
CREATE INDEX IF NOT EXISTS options_event_id ON options (event_id);
ALTER TABLE events ADD COLUMN IF NOT EXISTS raw jsonb;
ALTER TABLE events ADD COLUMN IF NOT EXISTS live boolean NOT NULL DEFAULT false;
ALTER TABLE events ADD COLUMN IF NOT EXISTS popular boolean NOT NULL DEFAULT false;`

var db *pgxpool.Pool

func initDB() {
	ctx := context.Background()
	var err error
	if db, err = pgxpool.New(ctx, env("DATABASE_URL", "")); err == nil {
		_, err = db.Exec(ctx, schema)
	}
	if err != nil {
		log.Fatalf("db: %v", err)
	}
}

// every: el "schedule:work" de Laravel. Corre job ya y después cada d, nunca solapado consigo mismo.
func every(d time.Duration, job func()) {
	go func() {
		for {
			job()
			time.Sleep(d)
		}
	}()
}

func syncEvents() {
	saveEvents("pelota", fetchPelota())
	if saveEvents("juanita", fetchJuanita()) > 0 {
		refreshGuide()
	}
	syncStreamed()
}

// syncStreamed: all + flags live/popular; si all falla no toca la BD (no apaga flags por una caída).
func syncStreamed() {
	all := fetchMatches("/matches/all")
	if all == nil {
		return
	}
	ids := func(ms []map[string]any) map[string]bool {
		set := map[string]bool{}
		for _, m := range ms {
			set[str(m["id"])] = true
		}
		return set
	}
	known, live := ids(all), fetchMatches("/matches/live")
	for _, m := range live {
		if !known[str(m["id"])] {
			all = append(all, m)
		}
	}
	popular := append(fetchMatches("/matches/all/popular"), fetchMatches("/matches/live/popular")...) // all/popular no siempre trae los live
	saveEvents("streamed", streamedEvents(all, ids(live), ids(popular)))
}

// saveEvents: upsert del evento + reemplazo de sus opciones (conserva el orden upstream, tira links muertos).
// ponytail: pelota no trae fecha → day = hoy AR; un sync pasada la medianoche duplica sus eventos hasta el prune.
// Devuelve cuántos eventos son nuevos (no estaban en la BD).
func saveEvents(provider string, events []Event) int {
	added := 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE events SET live = false, popular = false WHERE provider = $1`, provider); err != nil {
			return err
		}
		for _, e := range events {
			// upstream sigue listando partidos terminados: sin esto el prune los borra y el sync los revive.
			if t := startsAt(e); (len(e.Options) == 0 && e.Raw == nil) || (t != nil && t.Before(time.Now().Add(-pruneAfter))) {
				continue
			}
			var id int64
			var inserted bool
			err := tx.QueryRow(ctx, `INSERT INTO events (provider, day, starts_at, league, home, away, home_logo, away_logo, raw, live, popular)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (provider, day, home, away) DO UPDATE SET starts_at = EXCLUDED.starts_at, league = EXCLUDED.league,
	home_logo = EXCLUDED.home_logo, away_logo = EXCLUDED.away_logo, raw = EXCLUDED.raw,
	live = EXCLUDED.live, popular = EXCLUDED.popular, last_seen = now()
RETURNING id, xmax = 0`, provider, deref(e.Date, todayAR()), startsAt(e), e.League, e.Home, e.Away, e.HomeLogo, e.AwayLogo,
				e.Raw, e.Live, e.Popular).Scan(&id, &inserted)
			if err != nil {
				return err
			}
			if inserted {
				added++
			}
			if _, err := tx.Exec(ctx, `DELETE FROM options WHERE event_id = $1`, id); err != nil {
				return err
			}
			for _, o := range e.Options {
				if _, err := tx.Exec(ctx, `INSERT INTO options (event_id, source, quality, url, embed) VALUES ($1, $2, $3, $4, $5)`,
					id, o.Source, o.Quality, o.URL, o.Embed); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("sync %s: %v", provider, err)
		return 0
	}
	log.Printf("sync %s: %d events, %d nuevos", provider, len(events), added)
	return added
}

var (
	jellyfinURL = strings.TrimRight(env("JELLYFIN_URL", ""), "/")
	jellyfinKey = env("JELLYFIN_API_KEY", "")
)

// refreshGuide: corre "Actualizar la guía" de Jellyfin para que un partido nuevo aparezca ya, sin esperar
// la tarea programada. No-op sin JELLYFIN_URL/JELLYFIN_API_KEY.
func refreshGuide() {
	if jellyfinURL == "" || jellyfinKey == "" {
		return
	}
	auth := `MediaBrowser Token="` + jellyfinKey + `"`
	body, err := fetch(jellyfinURL+"/ScheduledTasks?isHidden=false", 1, 0, 10*time.Second, map[string]string{"Authorization": auth})
	var tasks []struct {
		ID  string `json:"Id"`
		Key string
	}
	if err == nil {
		err = json.Unmarshal(body, &tasks)
	}
	id := ""
	for _, t := range tasks {
		if t.Key == "RefreshGuide" {
			id = t.ID
		}
	}
	if err == nil && id == "" {
		err = errors.New("tarea RefreshGuide no encontrada")
	}
	if err == nil {
		req, _ := http.NewRequest(http.MethodPost, jellyfinURL+"/ScheduledTasks/Running/"+id, nil)
		req.Header.Set("Authorization", auth)
		var res *http.Response
		if res, err = (&http.Client{Timeout: 10 * time.Second}).Do(req); err == nil {
			res.Body.Close()
			if res.StatusCode > 299 {
				err = fmt.Errorf("POST RefreshGuide: %s", res.Status)
			}
		}
	}
	if err != nil {
		log.Printf("jellyfin: %v", err)
		return
	}
	log.Printf("jellyfin: actualizando guía")
}

// warmChannels: precalienta la grilla 24/7 canal por canal (no 715 requests de golpe); solo re-resuelve lo vencido.
// ponytail: ~715 opciones, las fallidas se reintentan cada 5m; filtrar canales muertos si upstream se queja.
func warmChannels() {
	for _, c := range getChannels() {
		resolveMany(optionURLs(c.Options))
	}
}

var pruneAfter = mustDuration(env("PRUNE_AFTER", "4h"))

// pruneEvents: borra eventos que empezaron hace más de PRUNE_AFTER; sin hora, los que upstream dejó de listar.
func pruneEvents() {
	tag, err := db.Exec(context.Background(), `DELETE FROM events WHERE coalesce(starts_at, last_seen) < $1`, time.Now().Add(-pruneAfter))
	if err != nil {
		log.Printf("prune: %v", err)
		return
	}
	log.Printf("prune: %d events", tag.RowsAffected())
}

func loadEvents(provider string) []Event {
	out := []Event{}
	rows, err := db.Query(context.Background(), `SELECT e.id, e.day, e.starts_at, e.league, e.home, e.away, e.home_logo, e.away_logo,
	o.source, o.quality, o.url, o.embed
FROM events e JOIN options o ON o.event_id = e.id
WHERE e.provider = $1
ORDER BY e.starts_at NULLS LAST, e.id, o.id`, provider)
	if err != nil {
		log.Printf("load %s: %v", provider, err)
		return out
	}
	defer rows.Close()
	var last int64
	for rows.Next() {
		var (
			id    int64
			day   time.Time
			start *time.Time
			e     Event
			o     Option
		)
		if err := rows.Scan(&id, &day, &start, &e.League, &e.Home, &e.Away, &e.HomeLogo, &e.AwayLogo,
			&o.Source, &o.Quality, &o.URL, &o.Embed); err != nil {
			log.Printf("load %s: %v", provider, err)
			return out
		}
		if id != last {
			e.Date = ptr(day.Format("2006-01-02"))
			if start != nil {
				e.Time = ptr(start.In(ar).Format("15:04"))
			}
			out, last = append(out, e), id
		}
		out[len(out)-1].Options = append(out[len(out)-1].Options, o)
	}
	if err := rows.Err(); err != nil {
		log.Printf("load %s: %v", provider, err)
	}
	return out
}

// streamedEvents: streamed no da URLs por evento, sino {source,id} → endpoint /stream de upstream.
func streamedEvents(matches []map[string]any, live, popular map[string]bool) []Event {
	out := []Event{}
	for _, m := range matches {
		title := str(m["name"])
		league, home, away, ok := matchup(title, ptr(str(m["category"])))
		if !ok {
			league, home = ptr(str(m["category"])), title
		}
		id := str(m["id"])
		e := Event{League: league, Home: home, Away: away, Raw: m, Live: live[id], Popular: popular[id]}
		if d := str(m["date"]); len(d) >= 16 {
			e.Date, e.Time = ptr(d[:10]), ptr(d[11:16])
		}
		srcs, _ := m["sources"].([]any)
		for _, s := range srcs {
			sm, _ := s.(map[string]any)
			src, sid := str(sm["source"]), str(sm["id"])
			if src == "" || sid == "" {
				continue
			}
			u := streamedAPI + "/stream/" + url.PathEscape(src) + "/" + url.PathEscape(sid)
			e.Options = append(e.Options, Option{Source: src, URL: u, Embed: u})
		}
		out = append(out, e)
	}
	return out
}

// startsAt: fecha (o hoy AR) + hora AR; nil sin hora.
func startsAt(e Event) *time.Time {
	if e.Time == nil {
		return nil
	}
	t, err := time.ParseInLocation("2006-01-02 15:04", deref(e.Date, todayAR())+" "+*e.Time, ar)
	if err != nil {
		return nil
	}
	return &t
}

func mustDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		log.Fatalf("duration %q: %v", s, err)
	}
	return d
}

func ptr(s string) *string { return &s }
