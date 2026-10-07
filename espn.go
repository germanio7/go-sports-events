package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Resultados vía la API no oficial de ESPN (sin key ni SLA): fútbol, básquet, tenis, etc.
// sport/league son los slugs de ESPN: soccer/arg.1, basketball/nba, tennis/atp…
var espnAPI = strings.TrimRight(env("ESPN_API_BASE", "https://site.api.espn.com/apis/site/v2/sports"), "/")

type Score struct {
	ID         string `json:"id"`
	Date       string `json:"date"`
	State      string `json:"state"`  // pre | in | post
	Detail     string `json:"detail"` // "45'", "Final", "Q3 5:12"…
	Tournament string `json:"tournament,omitempty"`
	Home       Side   `json:"home"`
	Away       Side   `json:"away"`
	// carreras (F1): sin local/visitante; sesión (FP1, Qual, Race…) y top 3
	Session string `json:"session,omitempty"`
	Podium  []Side `json:"podium,omitempty"`
}

type Side struct {
	Name   string `json:"name"`
	Score  string `json:"score"` // tenis: sets "6 4 7"
	Winner bool   `json:"winner"`
	Logo   string `json:"logo,omitempty"` // escudo del equipo; tenis: bandera del país
}

type espnCompetition struct {
	ID     string
	Date   string
	Type   struct{ Abbreviation string }
	Status struct {
		Type struct{ State, ShortDetail string }
	}
	Competitors []espnCompetitor
}

type espnCompetitor struct {
	HomeAway string
	Order    int
	Score    string
	Winner   bool
	Team     struct{ DisplayName, Logo string }
	Athlete  struct {
		DisplayName string
		Flag        struct{ Href string }
	}
	Linescores []struct{ Value float64 }
}

func getScores(sport, league string) []Score {
	return cached("espn:"+sport+":"+league, 30*time.Second, func() ([]Score, bool) {
		body, err := fetch(espnAPI+"/"+url.PathEscape(sport)+"/"+url.PathEscape(league)+"/scoreboard", 2, 800*time.Millisecond, 15*time.Second, nil)
		var scores []Score
		if err == nil {
			scores, err = parseESPN(body, league)
		}
		if err != nil {
			log.Printf("espn %s/%s failed: %v", sport, league, err)
			return []Score{}, false
		}
		return scores, true
	})
}

// parseESPN: deportes de equipo traen events[].competitions; tenis trae events[] = torneos con groupings[].competitions = partidos.
// Los torneos combinados traen los cuadros de ambos circuitos: atp se queda con mens-*, wta con womens-*.
func parseESPN(body []byte, league string) ([]Score, error) {
	var payload struct {
		Events []struct {
			Name         string
			Competitions []espnCompetition
			Groupings    []struct {
				Grouping     struct{ Slug string }
				Competitions []espnCompetition
			}
		}
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := []Score{}
	for _, e := range payload.Events {
		type comp struct {
			espnCompetition
			tournament string
		}
		comps := []comp{}
		for _, c := range e.Competitions {
			comps = append(comps, comp{c, ""})
		}
		for _, g := range e.Groupings {
			slug := g.Grouping.Slug
			if (league == "atp" && strings.HasPrefix(slug, "womens")) || (league == "wta" && strings.HasPrefix(slug, "mens")) {
				continue
			}
			name := e.Name
			if strings.HasSuffix(slug, "doubles") {
				name += " · dobles"
			}
			for _, c := range g.Competitions {
				comps = append(comps, comp{c, name})
			}
		}
		for _, cc := range comps {
			c := cc.espnCompetition
			s := Score{ID: c.ID, Date: c.Date, State: c.Status.Type.State, Detail: c.Status.Type.ShortDetail, Tournament: cc.tournament}
			if t, err := time.Parse("2006-01-02T15:04Z", c.Date); err == nil {
				s.Date = t.In(ar).Format("2006-01-02T15:04:05-07:00")
			}
			if len(c.Competitors) > 2 {
				s.Session, s.Tournament = c.Type.Abbreviation, e.Name
				if s.State != "pre" {
					ps := slices.Clone(c.Competitors)
					slices.SortFunc(ps, func(a, b espnCompetitor) int { return a.Order - b.Order })
					for _, p := range ps[:3] {
						s.Podium = append(s.Podium, Side{Name: p.Athlete.DisplayName, Winner: p.Winner, Logo: p.Athlete.Flag.Href})
					}
				}
				out = append(out, s)
				continue
			}
			for _, p := range c.Competitors {
				side := Side{Name: p.Team.DisplayName, Score: p.Score, Winner: p.Winner, Logo: p.Team.Logo}
				if side.Name == "" {
					side.Name, side.Logo = p.Athlete.DisplayName, p.Athlete.Flag.Href
				}
				if side.Score == "" && len(p.Linescores) > 0 {
					sets := make([]string, len(p.Linescores))
					for i, l := range p.Linescores {
						sets[i] = fmt.Sprintf("%g", l.Value)
					}
					side.Score = strings.Join(sets, " ")
				}
				if p.HomeAway == "home" {
					s.Home = side
				} else {
					s.Away = side
				}
			}
			out = append(out, s)
		}
	}
	return out, nil
}
