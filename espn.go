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
	// carreras (F1): sin local/visitante; sesión (FP1, Qual, Race…) y clasificación completa en orden
	Session string `json:"session,omitempty"`
	Results []Side `json:"results,omitempty"`
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
					for _, p := range ps {
						s.Results = append(s.Results, Side{Name: p.Athlete.DisplayName, Winner: p.Winner, Logo: p.Athlete.Flag.Href})
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

// ---- detalle de un partido (summary) ----
// Tenis y F1 no tienen summary en ESPN: F1 ya trae la clasificación en el scoreboard.

type Detail struct {
	Venue       string   `json:"venue,omitempty"` // "Spectrum Center · Charlotte"
	HomePeriods []string `json:"homePeriods,omitempty"`
	AwayPeriods []string `json:"awayPeriods,omitempty"`
	Plays       []Play   `json:"plays"`
	Stats       []Stat   `json:"stats"`
}

type Play struct {
	Clock string `json:"clock"` // "38'", "Q1 13:34"
	Side  string `json:"side"`  // home | away
	Kind  string `json:"kind"`  // goal | yellow | red | score
	Text  string `json:"text"`
}

type Stat struct {
	Label string `json:"label"`
	Home  string `json:"home"`
	Away  string `json:"away"`
}

// Estadísticas que se muestran, en orden; los nombres son de ESPN y no chocan entre deportes.
var statLabels = []struct{ name, label string }{
	{"possessionPct", "Posesión %"}, {"totalShots", "Remates"}, {"shotsOnTarget", "Al arco"}, {"wonCorners", "Córners"},
	{"foulsCommitted", "Faltas"}, {"yellowCards", "Amarillas"}, {"redCards", "Rojas"}, {"offsides", "Offsides"}, {"saves", "Atajadas"},
	{"fieldGoalsMade-fieldGoalsAttempted", "Tiros de campo"}, {"threePointFieldGoalsMade-threePointFieldGoalsAttempted", "Triples"},
	{"freeThrowsMade-freeThrowsAttempted", "Libres"}, {"totalRebounds", "Rebotes"}, {"assists", "Asistencias"},
	{"steals", "Robos"}, {"blocks", "Tapas"},
	{"totalYards", "Yardas totales"}, {"netPassingYards", "Yardas por pase"}, {"rushingYards", "Yardas por tierra"},
	{"firstDowns", "Primeros downs"}, {"thirdDownEff", "Tercer down"}, {"possessionTime", "Tiempo de posesión"},
	{"turnovers", "Pérdidas"},
}

func getDetail(sport, league, id string) (Detail, bool) {
	type res struct {
		d  Detail
		ok bool
	}
	r := cached("espn:detail:"+sport+":"+league+":"+id, 30*time.Second, func() (res, bool) {
		u := espnAPI + "/" + url.PathEscape(sport) + "/" + url.PathEscape(league) + "/summary?event=" + url.QueryEscape(id)
		body, err := fetch(u, 2, 800*time.Millisecond, 15*time.Second, nil)
		var d Detail
		if err == nil {
			d, err = parseSummary(body)
		}
		if err != nil {
			log.Printf("espn summary %s/%s/%s failed: %v", sport, league, id, err)
			return res{}, false
		}
		return res{d, true}, true
	})
	return r.d, r.ok
}

func parseSummary(body []byte) (Detail, error) {
	type team struct{ ID string }
	var p struct {
		GameInfo struct {
			Venue struct {
				FullName string
				Address  struct{ City string }
			}
		}
		Header struct {
			Competitions []struct {
				Competitors []struct {
					HomeAway   string
					Team       team
					Linescores []struct{ DisplayValue string }
				}
			}
		}
		Boxscore struct {
			Teams []struct {
				Team       team
				Statistics []struct{ Name, DisplayValue string }
			}
		}
		KeyEvents []struct {
			Type         struct{ Type string }
			Clock        struct{ DisplayValue string }
			Team         team
			Participants []struct{ Athlete struct{ DisplayName string } }
		}
		ScoringPlays []struct {
			Text   string
			Clock  struct{ DisplayValue string }
			Period struct{ Number int }
			Team   team
		}
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Detail{}, err
	}

	d := Detail{Venue: p.GameInfo.Venue.FullName, Plays: []Play{}, Stats: []Stat{}}
	if c := p.GameInfo.Venue.Address.City; c != "" && d.Venue != "" {
		d.Venue += " · " + c
	}

	side := map[string]string{} // team id → home | away
	if len(p.Header.Competitions) > 0 {
		for _, c := range p.Header.Competitions[0].Competitors {
			side[c.Team.ID] = c.HomeAway
			periods := make([]string, len(c.Linescores))
			for i, l := range c.Linescores {
				periods[i] = l.DisplayValue
			}
			if c.HomeAway == "home" {
				d.HomePeriods = periods
			} else {
				d.AwayPeriods = periods
			}
		}
	}

	// fútbol: goles y tarjetas (type.type: "goal", "goal---free-kick", "own-goal", "yellow-card"…)
	for _, k := range p.KeyEvents {
		kind := ""
		switch t := k.Type.Type; {
		case strings.Contains(t, "goal"):
			kind = "goal"
		case strings.HasPrefix(t, "yellow-card"):
			kind = "yellow"
		case strings.HasPrefix(t, "red-card"):
			kind = "red"
		default:
			continue
		}
		text := ""
		if len(k.Participants) > 0 {
			text = k.Participants[0].Athlete.DisplayName
		}
		if k.Type.Type == "own-goal" {
			text += " (e/c)"
		}
		d.Plays = append(d.Plays, Play{Clock: k.Clock.DisplayValue, Side: side[k.Team.ID], Kind: kind, Text: text})
	}
	// NFL: jugadas de puntos
	for _, sp := range p.ScoringPlays {
		d.Plays = append(d.Plays, Play{Clock: fmt.Sprintf("Q%d %s", sp.Period.Number, sp.Clock.DisplayValue), Side: side[sp.Team.ID], Kind: "score", Text: sp.Text})
	}

	values := map[string]map[string]string{} // home|away → stat name → valor
	for _, t := range p.Boxscore.Teams {
		m := map[string]string{}
		for _, s := range t.Statistics {
			if _, dup := m[s.Name]; !dup { // NFL repite "interceptions"
				m[s.Name] = s.DisplayValue
			}
		}
		values[side[t.Team.ID]] = m
	}
	for _, l := range statLabels {
		h, okH := values["home"][l.name]
		a, okA := values["away"][l.name]
		if okH && okA {
			d.Stats = append(d.Stats, Stat{l.label, h, a})
		}
	}
	return d, nil
}
