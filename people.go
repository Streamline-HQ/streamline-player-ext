package main

import (
	"net/http"
	"strconv"

	"github.com/datahearth/streamline/extensions/sdk"
)

const knownForLimit = 5

func init() {
	handleRoute(http.MethodGet, "/people/{id}", Any, getPerson)
}

type personCredit struct {
	Kind         string `json:"kind"`
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Year         int64  `json:"year"`
	Character    string `json:"character"`
	TotalSeasons *int   `json:"total_seasons"`
}

type knownFor struct {
	Title string `json:"title"`
	Year  int64  `json:"year"`
	Kind  string `json:"kind"`
	ID    int64  `json:"id"`
}

type person struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Department string         `json:"department"`
	Credits    []personCredit `json:"credits"`
	KnownFor   []knownFor     `json:"known_for"`
}

func getPerson(c *call) sdk.Response {
	id, ok := pathID(c)
	if !ok {
		return libraryNotFound()
	}
	res, err := query(
		`SELECT id, name, known_for FROM streamline_people WHERE id = ?`, id)
	if err != nil {
		return libraryFailed(err)
	}
	if len(res.Rows) == 0 {
		return libraryNotFound()
	}
	r := row(res.Rows[0])
	p := person{
		ID: strconv.FormatInt(id, 10), Name: r.str(1), Department: r.str(2),
		Credits: []personCredit{}, KnownFor: []knownFor{},
	}
	if p.Department == "" {
		p.Department = "Acting"
	}

	rows, err := query(
		`SELECT 'movies', m.id, m.title, m.year, c.character, 0, m.rating
		FROM streamline_credits c JOIN streamline_movies m ON m.id = c.movie_id
		WHERE c.person_id = ?1 AND c.job = ''
			AND `+withFile+`f.movie_id = m.id)
		UNION ALL
		SELECT 'series', s.id, s.title, s.year, c.character,
			`+seasonsWithFiles+`,
			s.rating
		FROM streamline_credits c JOIN streamline_series s ON s.id = c.series_id
		WHERE c.person_id = ?1 AND c.job = ''
			AND EXISTS(SELECT 1 FROM streamline_seasons se
				JOIN streamline_episodes e ON e.season_id = se.id
				WHERE se.series_id = s.id AND `+withFile+`f.episode_id = e.id))
		ORDER BY 4 DESC, 3`, id)
	if err != nil {
		return libraryFailed(err)
	}
	type rated struct {
		knownFor
		rating float64
	}
	var best []rated
	seen := map[string]bool{}
	for _, cr := range rows.Rows {
		r := row(cr)
		kind, tid := r.str(0), r.int(1)
		key := kind + strconv.FormatInt(tid, 10)
		if seen[key] {
			continue
		}
		seen[key] = true
		credit := personCredit{
			Kind: kind, ID: tid, Title: r.str(2), Year: r.int(3),
			Character: r.str(4),
		}
		if kind == "series" {
			n := int(r.int(5))
			credit.TotalSeasons = &n
		}
		p.Credits = append(p.Credits, credit)
		best = append(best, rated{
			knownFor{Title: credit.Title, Year: credit.Year, Kind: kind, ID: tid},
			libFloat(r[6]),
		})
	}
	for len(p.KnownFor) < knownForLimit && len(best) > 0 {
		top := 0
		for i, b := range best {
			if b.rating > best[top].rating {
				top = i
			}
		}
		p.KnownFor = append(p.KnownFor, best[top].knownFor)
		best = append(best[:top], best[top+1:]...)
	}
	return jsonOK(http.StatusOK, p)
}
