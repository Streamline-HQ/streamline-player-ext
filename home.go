package main

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/datahearth/streamline/extensions/sdk"
)

const (
	featuredMax  = 5
	arrivalDays  = 14
	dateSlackDay = 2
)

func init() {
	handleRoute(http.MethodGet, "/home", Any, getHome)
}

type featured struct {
	Kind     string  `json:"kind"`
	ID       int64   `json:"id"`
	Reason   string  `json:"reason"`
	Season   *int64  `json:"season,omitempty"`
	Episodes []int64 `json:"episodes,omitempty"`
}

type arrival struct {
	Kind     string  `json:"kind"`
	ID       int64   `json:"id"`
	At       string  `json:"at"`
	Season   *int64  `json:"season,omitempty"`
	Episodes []int64 `json:"episodes,omitempty"`

	when time.Time
}

type arrivalDay struct {
	Date  string    `json:"date"`
	Items []arrival `json:"items"`
}

type titleKey struct {
	kind string
	id   int64
}

// coreTimeLayouts are the forms a core timestamp reaches the guest in: the
// host formats times as RFC3339, text columns keep what the writer stored.
var coreTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05",
}

func parseCoreTime(v any) (time.Time, bool) {
	s, _ := v.(string)
	for _, layout := range coreTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// dateFloor is a YYYY-MM-DD string no later than the day of t in any zone, to
// narrow a query on a timestamp column that holds text in mixed zones.
func dateFloor(t time.Time) string {
	return t.UTC().AddDate(0, 0, -dateSlackDay).Format(time.DateOnly)
}

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func getHome(c *call) sdk.Response {
	t := now()
	var feed []featured
	seen := map[titleKey]bool{}
	add := func(f featured) {
		k := titleKey{f.Kind, f.ID}
		if len(feed) < featuredMax && !seen[k] {
			seen[k] = true
			feed = append(feed, f)
		}
	}

	cont, err := continueWatching(c.User.ID)
	if err != nil {
		return internalError("reading continue watching", err)
	}
	fresh, err := newEpisodes(c.User.ID)
	if err != nil {
		return internalError("reading new episodes", err)
	}
	freshBy := map[int64]featured{}
	for _, f := range fresh {
		freshBy[f.ID] = f
	}
	for _, f := range cont {
		if n, ok := freshBy[f.ID]; ok && f.Reason == "next" {
			f = n
		}
		add(f)
	}
	for _, f := range fresh {
		add(f)
	}
	added, err := newestTitles()
	if err != nil {
		return internalError("reading the newest titles", err)
	}
	for _, f := range added {
		add(f)
	}
	arrivals, err := recentArrivals(t)
	if err != nil {
		return internalError("reading arrivals", err)
	}
	if feed == nil {
		feed = []featured{}
	}
	return jsonOK(http.StatusOK, map[string]any{
		"featured": feed, "arrivals": arrivals,
	})
}

// continueWatching lists the unfinished plays, newest first. A series is
// decided by its newest play: unfinished, it continues; finished, the next
// episode with a file the user has not watched is up next.
func continueWatching(userID uint32) ([]featured, error) {
	res, err := query(
		`SELECT f.movie_id, se.series_id, se.number, e.number, w.watched
		FROM watch w
		JOIN streamline_video_files f ON f.id = w.media_id
		LEFT JOIN streamline_episodes e ON e.id = f.episode_id
		LEFT JOIN streamline_seasons se ON se.id = e.season_id
		WHERE w.user_id = ? AND (w.watched = 1 AND se.series_id IS NOT NULL
			OR w.watched = 0 AND w.position > 0 AND w.position < ? * w.duration)
		ORDER BY w.updated_at DESC, w.rowid DESC`,
		userID, watchedAt,
	)
	if err != nil {
		return nil, err
	}
	var out []featured
	decided := map[int64]bool{}
	for _, raw := range res.Rows {
		if len(out) == featuredMax {
			break
		}
		r := row(raw)
		if r[0] != nil {
			out = append(
				out,
				featured{Kind: "movies", ID: r.int(0), Reason: "continue"},
			)
			continue
		}
		series := r.int(1)
		if decided[series] {
			continue
		}
		decided[series] = true
		season := r.int(2)
		if r.int(4) == 0 {
			out = append(out, featured{
				Kind: "series", ID: series, Reason: "continue", Season: &season,
			})
			continue
		}
		next, ok, err := nextUp(userID, series, season, r.int(3))
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, featured{
				Kind: "series", ID: series, Reason: "next", Season: &next,
			})
		}
	}
	return out, nil
}

// nextUp is the season of the first episode after season/episode that has a
// file the user has not watched.
func nextUp(userID uint32, series, season, episode int64) (int64, bool, error) {
	res, err := query(
		`SELECT se.number FROM streamline_episodes e
		JOIN streamline_seasons se ON se.id = e.season_id
		WHERE se.series_id = ? AND (se.number > ? OR se.number = ? AND e.number > ?)
			AND EXISTS(SELECT 1 FROM streamline_video_files f
				WHERE f.episode_id = e.id AND f.missing = 0 AND NOT EXISTS(
					SELECT 1 FROM watch w
					WHERE w.user_id = ? AND w.media_id = f.id AND w.watched = 1))
		ORDER BY se.number, e.number LIMIT 1`,
		series, season, season, episode, userID,
	)
	if err != nil || len(res.Rows) == 0 {
		return 0, false, err
	}
	return row(res.Rows[0]).int(0), true, nil
}

// newEpisodes lists the shows the user has watched that gained an episode
// after their latest watch row for the show, the show with the newest
// episode first.
func newEpisodes(userID uint32) ([]featured, error) {
	last, err := query(
		`SELECT se.series_id, MAX(w.updated_at)
		FROM watch w
		JOIN streamline_video_files f ON f.id = w.media_id
		JOIN streamline_episodes e ON e.id = f.episode_id
		JOIN streamline_seasons se ON se.id = e.season_id
		WHERE w.user_id = ?
		GROUP BY se.series_id`, userID,
	)
	if err != nil || len(last.Rows) == 0 {
		return nil, err
	}
	watchedUntil := map[int64]time.Time{}
	earliest := now()
	for _, raw := range last.Rows {
		r := row(raw)
		at := time.UnixMilli(r.int(1))
		watchedUntil[r.int(0)] = at
		earliest = minTime(earliest, at)
	}
	res, err := query(
		`SELECT se.series_id, se.number, e.number, e.added_at
		FROM streamline_episodes e
		JOIN streamline_seasons se ON se.id = e.season_id
		WHERE e.added_at >= ? AND EXISTS(SELECT 1 FROM streamline_video_files f
			WHERE f.episode_id = e.id AND f.missing = 0)`,
		dateFloor(earliest),
	)
	if err != nil {
		return nil, err
	}
	type fresh struct {
		newest time.Time
		season int64
		by     map[int64][]int64
	}
	shows := map[int64]*fresh{}
	for _, raw := range res.Rows {
		r := row(raw)
		series := r.int(0)
		seen, watched := watchedUntil[series]
		added, ok := parseCoreTime(r[3])
		if !watched || !ok || !added.After(seen) {
			continue
		}
		f := shows[series]
		if f == nil {
			f = &fresh{by: map[int64][]int64{}}
			shows[series] = f
		}
		f.by[r.int(1)] = append(f.by[r.int(1)], r.int(2))
		if added.After(f.newest) {
			f.newest, f.season = added, r.int(1)
		}
	}
	ids := make([]int64, 0, len(shows))
	for id := range shows {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b int64) int {
		if c := shows[b].newest.Compare(shows[a].newest); c != 0 {
			return c
		}
		return int(a - b)
	})
	out := make([]featured, 0, len(ids))
	for _, id := range ids {
		f := shows[id]
		eps := f.by[f.season]
		slices.Sort(eps)
		season := f.season
		out = append(out, featured{
			Kind: "series", ID: id, Reason: "new_episodes",
			Season: &season, Episodes: eps,
		})
	}
	return out, nil
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

const withFile = `EXISTS(SELECT 1 FROM streamline_video_files f
	WHERE f.missing = 0 AND `

// newestTitles lists the titles with a file, newest added first.
func newestTitles() ([]featured, error) {
	type dated struct {
		f  featured
		at time.Time
	}
	var all []dated
	for _, src := range []struct{ kind, sql string }{
		{"movies", `SELECT m.id, m.added_at FROM streamline_movies m
			WHERE ` + withFile + `f.movie_id = m.id)
			ORDER BY m.added_at DESC LIMIT 20`},
		{"series", `SELECT s.id, s.added_at FROM streamline_series s
			WHERE EXISTS(SELECT 1 FROM streamline_seasons se
				JOIN streamline_episodes e ON e.season_id = se.id
				WHERE se.series_id = s.id AND ` + withFile + `f.episode_id = e.id))
			ORDER BY s.added_at DESC LIMIT 20`},
	} {
		res, err := query(src.sql)
		if err != nil {
			return nil, err
		}
		for _, raw := range res.Rows {
			r := row(raw)
			if at, ok := parseCoreTime(r[1]); ok {
				all = append(all, dated{
					f:  featured{Kind: src.kind, ID: r.int(0), Reason: "added"},
					at: at,
				})
			}
		}
	}
	slices.SortStableFunc(all, func(a, b dated) int { return b.at.Compare(a.at) })
	out := make([]featured, len(all))
	for i, d := range all {
		out[i] = d.f
	}
	return out, nil
}

func localDay(t time.Time) string { return t.In(time.Local).Format(time.DateOnly) }

// recentArrivals groups what the library gained in the last arrivalDays days
// by local day, newest first. A title is an arrival of its own on the day it
// was added; episodes that came on another day are an arrival of their
// season.
func recentArrivals(t time.Time) ([]arrivalDay, error) {
	from := t.In(time.Local).AddDate(0, 0, -arrivalDays)
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.Local)
	floor := dateFloor(from)

	var items []arrival

	titles, err := query(
		`SELECT 'movies', m.id, m.added_at FROM streamline_movies m
		WHERE m.added_at >= ? AND `+withFile+`f.movie_id = m.id)
		UNION ALL
		SELECT 'series', s.id, s.added_at FROM streamline_series s
		WHERE s.added_at >= ? AND EXISTS(SELECT 1 FROM streamline_seasons se
			JOIN streamline_episodes e ON e.season_id = se.id
			WHERE se.series_id = s.id AND `+withFile+`f.episode_id = e.id))`,
		floor, floor,
	)
	if err != nil {
		return nil, err
	}
	for _, raw := range titles.Rows {
		r := row(raw)
		at, ok := parseCoreTime(r[2])
		if !ok {
			continue
		}
		if !at.Before(from) {
			items = append(items, arrival{
				Kind: r.str(0), ID: r.int(1), At: utc(at), when: at,
			})
		}
	}

	eps, err := query(
		`SELECT se.series_id, se.number, e.number, e.added_at, s.added_at
		FROM streamline_episodes e
		JOIN streamline_seasons se ON se.id = e.season_id
		JOIN streamline_series s ON s.id = se.series_id
		WHERE e.added_at >= ? AND `+withFile+`f.episode_id = e.id)`, floor,
	)
	if err != nil {
		return nil, err
	}
	type groupKey struct {
		series, season int64
		day            string
	}
	groups := map[groupKey]*arrival{}
	for _, raw := range eps.Rows {
		r := row(raw)
		at, ok := parseCoreTime(r[3])
		if !ok || at.Before(from) {
			continue
		}
		if sa, ok := parseCoreTime(r[4]); ok && localDay(sa) == localDay(at) {
			continue
		}
		k := groupKey{r.int(0), r.int(1), localDay(at)}
		g := groups[k]
		if g == nil {
			season := r.int(1)
			g = &arrival{Kind: "series", ID: r.int(0), Season: &season}
			groups[k] = g
		}
		g.Episodes = append(g.Episodes, r.int(2))
		if at.After(g.when) {
			g.when, g.At = at, utc(at)
		}
	}
	for _, g := range groups {
		slices.Sort(g.Episodes)
		items = append(items, *g)
	}

	slices.SortStableFunc(items, func(a, b arrival) int {
		if c := b.when.Compare(a.when); c != 0 {
			return c
		}
		if c := strings.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return int(a.ID - b.ID)
	})
	days := []arrivalDay{}
	for _, it := range items {
		d := localDay(it.when)
		if n := len(days); n == 0 || days[n-1].Date != d {
			days = append(days, arrivalDay{Date: d})
		}
		days[len(days)-1].Items = append(days[len(days)-1].Items, it)
	}
	return days, nil
}
