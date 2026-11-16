package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/datahearth/streamline/extensions/sdk"
)

func init() {
	handleRoute(http.MethodGet, "/movies", Any, listMovies)
	handleRoute(http.MethodGet, "/series", Any, listSeries)
	handleRoute(http.MethodGet, "/movies/{id}", Any, getMovie)
	handleRoute(http.MethodGet, "/series/{id}", Any, getSeries)
}

type fileSummary struct {
	FileCount  int64  `json:"file_count"`
	Resolution string `json:"resolution,omitempty"`
	Codec      string `json:"codec,omitempty"`
	HDR        string `json:"hdr,omitempty"`
	Audio      string `json:"audio,omitempty"`
	Channels   string `json:"channels,omitempty"`
	Size       int64  `json:"size"`
}

type castMember struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Character string `json:"character"`
}

type episode struct {
	ID          int64  `json:"id"`
	Number      int64  `json:"number"`
	Title       string `json:"title"`
	Overview    string `json:"overview"`
	Runtime     int64  `json:"runtime"`
	HasFile     bool   `json:"has_file"`
	MediaFileID string `json:"media_file_id"`
	AirDate     string `json:"air_date,omitempty"`
	Status      string `json:"status,omitempty"`
}

type season struct {
	Number   int64     `json:"number"`
	Name     string    `json:"name"`
	Total    int       `json:"total"`
	Episodes []episode `json:"episodes"`
}

type mediaFileRef struct {
	ID string `json:"id"`
}

type title struct {
	ID            int64          `json:"id"`
	Title         string         `json:"title"`
	OriginalTitle string         `json:"original_title"`
	Year          int64          `json:"year"`
	Overview      string         `json:"overview"`
	Genres        []string       `json:"genres"`
	Rating        float64        `json:"rating"`
	AddedAt       string         `json:"added_at"`
	Status        string         `json:"status,omitempty"`
	Monitored     bool           `json:"monitored"`
	TmdbID        int64          `json:"tmdb_id,omitempty"`
	FileSummary   *fileSummary   `json:"file_summary,omitempty"`
	Runtime       int64          `json:"runtime"`
	Certification string         `json:"certification,omitempty"`
	MediaFiles    []mediaFileRef `json:"media_files,omitempty"`
	Network       string         `json:"network,omitempty"`
	TotalSeasons  *int           `json:"total_seasons,omitempty"`
	FirstAired    string         `json:"first_aired,omitempty"`
	Seasons       []season       `json:"seasons,omitempty"`
	Cast          []castMember   `json:"cast,omitempty"`
}

type titleList struct {
	Items []title `json:"items"`
	Total int     `json:"total"`
}

const titleFileColumns = `a.cnt, a.size, f.quality, f.height, f.video_codec,
	f.dynamic_range, f.audio_codec, f.channels`

const movieSelect = `SELECT m.id, m.title, m.original_title, m.year, m.overview,
	m.runtime, m.rating, m.genres, m.monitored, m.certification, m.added_at,
	m.tmdb_id, '', '', '', ` + titleFileColumns + `
FROM streamline_movies m
LEFT JOIN (
	SELECT movie_id AS owner, count(*) AS cnt, sum(size) AS size, min(id) AS first
	FROM streamline_video_files
	WHERE missing = 0 AND movie_id IS NOT NULL
	GROUP BY movie_id
) a ON a.owner = m.id
LEFT JOIN streamline_video_files f ON f.id = a.first`

const seriesSelect = `SELECT s.id, s.title, s.original_title, s.year, s.overview,
	s.runtime, s.rating, s.genres, s.monitored, s.certification, s.added_at,
	(SELECT count(*) FROM streamline_seasons WHERE series_id = s.id),
	s.status, s.network, s.first_aired, ` + titleFileColumns + `
FROM streamline_series s
LEFT JOIN (
	SELECT se.series_id AS owner, count(*) AS cnt, sum(vf.size) AS size,
		min(vf.id) AS first
	FROM streamline_video_files vf
	JOIN streamline_episodes e ON e.id = vf.episode_id
	JOIN streamline_seasons se ON se.id = e.season_id
	WHERE vf.missing = 0
	GROUP BY se.series_id
) a ON a.owner = s.id
LEFT JOIN streamline_video_files f ON f.id = a.first`

func listMovies(c *call) sdk.Response { return listTitles(movieSelect, false) }

func listSeries(c *call) sdk.Response { return listTitles(seriesSelect, true) }

func listTitles(selectSQL string, show bool) sdk.Response {
	res, err := query(selectSQL + " ORDER BY 1")
	if err != nil {
		return libraryFailed(err)
	}
	out := titleList{Items: make([]title, 0, len(res.Rows)), Total: len(res.Rows)}
	for _, r := range res.Rows {
		out.Items = append(out.Items, titleOf(row(r), show))
	}
	return jsonOK(http.StatusOK, out)
}

func getMovie(c *call) sdk.Response {
	id, ok := pathID(c)
	if !ok {
		return libraryNotFound()
	}
	res, err := query(movieSelect+" WHERE m.id = ?", id)
	if err != nil {
		return libraryFailed(err)
	}
	if len(res.Rows) == 0 {
		return libraryNotFound()
	}
	t := titleOf(row(res.Rows[0]), false)
	files, err := query(
		`SELECT id FROM streamline_video_files
		WHERE movie_id = ? AND missing = 0 ORDER BY id`, id)
	if err != nil {
		return libraryFailed(err)
	}
	t.MediaFiles = []mediaFileRef{}
	for _, f := range files.Rows {
		t.MediaFiles = append(
			t.MediaFiles,
			mediaFileRef{ID: fileIDString(row(f).int(0))},
		)
	}
	if t.Cast, err = castOf("movie_id", id); err != nil {
		return libraryFailed(err)
	}
	return jsonOK(http.StatusOK, t)
}

func getSeries(c *call) sdk.Response {
	id, ok := pathID(c)
	if !ok {
		return libraryNotFound()
	}
	res, err := query(seriesSelect+" WHERE s.id = ?", id)
	if err != nil {
		return libraryFailed(err)
	}
	if len(res.Rows) == 0 {
		return libraryNotFound()
	}
	t := titleOf(row(res.Rows[0]), true)
	if t.Seasons, err = seasonsOf(id, t.Runtime); err != nil {
		return libraryFailed(err)
	}
	if t.Cast, err = castOf("series_id", id); err != nil {
		return libraryFailed(err)
	}
	return jsonOK(http.StatusOK, t)
}

func seasonsOf(seriesID, runtime int64) ([]season, error) {
	seasons, err := query(
		`SELECT id, number, name FROM streamline_seasons
		WHERE series_id = ? ORDER BY number, id`, seriesID)
	if err != nil {
		return nil, err
	}
	episodes, err := query(
		`SELECT e.season_id, e.id, e.number, e.title, e.overview, e.air_date,
			(SELECT min(id) FROM streamline_video_files
				WHERE episode_id = e.id AND missing = 0)
		FROM streamline_episodes e
		JOIN streamline_seasons se ON se.id = e.season_id
		WHERE se.series_id = ? ORDER BY e.number, e.id`, seriesID)
	if err != nil {
		return nil, err
	}
	out := make([]season, 0, len(seasons.Rows))
	index := map[int64]int{}
	for _, s := range seasons.Rows {
		r := row(s)
		index[r.int(0)] = len(out)
		out = append(out, season{
			Number: r.int(1), Name: r.str(2), Episodes: []episode{},
		})
	}
	for _, e := range episodes.Rows {
		r := row(e)
		at, ok := index[r.int(0)]
		if !ok {
			continue
		}
		ep := episode{
			ID: r.int(1), Number: r.int(2), Title: r.str(3), Overview: r.str(4),
			Runtime: runtime, AirDate: libDate(r[5]),
		}
		if file := r.int(6); file != 0 {
			ep.HasFile = true
			ep.MediaFileID = fileIDString(file)
			ep.Status = "available"
		}
		out[at].Episodes = append(out[at].Episodes, ep)
	}
	for i := range out {
		out[i].Total = len(out[i].Episodes)
	}
	return out, nil
}

// castOf lists the acting credits of one title; column is the credits view's
// owner column.
func castOf(column string, id int64) ([]castMember, error) {
	res, err := query(
		`SELECT p.id, p.name, c.character
		FROM streamline_credits c
		JOIN streamline_people p ON p.id = c.person_id
		WHERE c.`+column+` = ? AND c.job = ''
		ORDER BY c."order", c.id`, id)
	if err != nil {
		return nil, err
	}
	out := make([]castMember, 0, len(res.Rows))
	for _, cr := range res.Rows {
		r := row(cr)
		out = append(out, castMember{
			ID: strconv.FormatInt(r.int(0), 10), Name: r.str(1), Character: r.str(2),
		})
	}
	return out, nil
}

func titleOf(r row, show bool) title {
	t := title{
		ID: r.int(0), Title: r.str(1), OriginalTitle: r.str(2), Year: r.int(3),
		Overview: r.str(4), Runtime: r.int(5), Rating: libFloat(r[6]),
		Genres: libGenres(r.str(7)), Monitored: r.int(8) != 0,
		Certification: r.str(9), AddedAt: libStamp(r[10]),
	}
	if show {
		seasons := int(r.int(11))
		t.TotalSeasons = &seasons
		t.Status = r.str(12)
		t.Network = r.str(13)
		t.FirstAired = libDate(r[14])
	} else {
		t.TmdbID = r.int(11)
	}
	if r.int(15) > 0 {
		t.FileSummary = &fileSummary{
			FileCount: r.int(15), Size: r.int(16),
			Resolution: libResolution(r.str(17), r.int(18)),
			Codec:      r.str(19), HDR: r.str(20), Audio: r.str(21),
			Channels: libChannels(r.int(22)),
		}
	} else {
		t.FileSummary = &fileSummary{}
	}
	return t
}

func pathID(c *call) (int64, bool) {
	id, err := strconv.ParseInt(c.Params["id"], 10, 64)
	return id, err == nil && id > 0
}

func fileIDString(id int64) string { return strconv.FormatInt(id, 10) }

func libraryNotFound() sdk.Response {
	return fail(http.StatusNotFound, "not_found", "Not found")
}

func libraryFailed(err error) sdk.Response {
	sdk.Log("error", "reading the library failed", "error", err.Error())
	return fail(http.StatusInternalServerError, "internal", "Internal error")
}

func libFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}

func libGenres(raw string) []string {
	out := []string{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func libResolution(quality string, height int64) string {
	if quality != "" {
		return quality
	}
	if height > 0 {
		return strconv.FormatInt(height, 10) + "p"
	}
	return ""
}

func libChannels(n int64) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return "1.0"
	case n == 2:
		return "2.0"
	case n >= 6:
		return strconv.FormatInt(n-1, 10) + ".1"
	}
	return strconv.FormatInt(n, 10) + ".0"
}

var libTimeLayouts = []string{
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func libParse(v any) (time.Time, bool) {
	s, _ := v.(string)
	s = strings.TrimSpace(s)
	for _, layout := range libTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// libStamp is a core timestamp as the contract writes one.
func libStamp(v any) string {
	if t, ok := libParse(v); ok {
		return t.Format("2006-01-02T15:04:05.000Z")
	}
	s, _ := v.(string)
	return s
}

func libDate(v any) string {
	if t, ok := libParse(v); ok {
		return t.Format("2006-01-02")
	}
	s, _ := v.(string)
	return s
}
