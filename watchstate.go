package main

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/datahearth/streamline/extensions/sdk"
)

// watchedAt is the share of a title after which it counts as watched.
const (
	watchedAt = 0.9
	// watch.updated_at is in Unix milliseconds, so two marks in one second
	// still order.
	watchTimeLayout = "2006-01-02T15:04:05.000Z07:00"
)

func init() {
	registerSchema(
		`CREATE TABLE IF NOT EXISTS watch(
			user_id INTEGER NOT NULL,
			media_id INTEGER NOT NULL,
			position REAL NOT NULL DEFAULT 0,
			duration REAL NOT NULL DEFAULT 0,
			watched INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(user_id, media_id)
		)`,
		`CREATE INDEX IF NOT EXISTS watch_recent ON watch(user_id, updated_at)`,
	)
	handleRoute(http.MethodGet, "/watchstate", Any, listWatchState)
	handleRoute(http.MethodPut, "/watchstate/{mediaId}", Any, putWatchState)
	handleRoute(http.MethodPost, "/admin/watchstate/import", Admin, importWatchState)
}

const watchColumns = `w.media_id, w.position, w.duration, w.watched,
	w.updated_at, f.movie_id, f.episode_id, se.series_id, se.number`

const watchJoins = `FROM watch w
	JOIN streamline_video_files f ON f.id = w.media_id
	LEFT JOIN streamline_episodes e ON e.id = f.episode_id
	LEFT JOIN streamline_seasons se ON se.id = e.season_id`

func watchItem(r row) map[string]any {
	item := map[string]any{
		"media_id":         strconv.FormatInt(r.int(0), 10),
		"position_seconds": r[1],
		"duration_seconds": r[2],
		"watched":          r.int(3) != 0,
		"updated_at":       time.UnixMilli(r.int(4)).UTC().Format(watchTimeLayout),
	}
	if r[5] != nil {
		item["movie_id"] = r.int(5)
	}
	if r[7] != nil {
		item["series_id"] = r.int(7)
		item["season"] = r.int(8)
		item["episode_id"] = r.int(6)
	}
	return item
}

func listWatchState(c *call) sdk.Response {
	res, err := query(
		`SELECT `+watchColumns+` `+watchJoins+`
		WHERE w.user_id = ? ORDER BY w.updated_at DESC, w.rowid DESC`,
		c.User.ID,
	)
	if err != nil {
		return internalError("listing watch state", err)
	}
	items := make([]map[string]any, 0, len(res.Rows))
	for _, r := range res.Rows {
		items = append(items, watchItem(r))
	}
	return jsonOK(http.StatusOK, map[string]any{"items": items})
}

func putWatchState(c *call) sdk.Response {
	mediaID, err := strconv.ParseInt(c.Params["mediaId"], 10, 64)
	if err != nil {
		return fail(http.StatusNotFound, "not_found", "No such media")
	}
	var body struct {
		Watched  *bool  `json:"watched"`
		Position *int64 `json:"position_seconds"`
		Duration *int64 `json:"duration_seconds"`
		At       string `json:"at"`
	}
	invalid := fail(http.StatusBadRequest, "invalid_request",
		"The body must carry watched, or position_seconds, duration_seconds and at")
	if err := decodeBody(c, &body); err != nil {
		return invalid
	}
	offline := body.Watched == nil
	var at time.Time
	if offline {
		var ok bool
		at, ok = parseOfflineTime(body.At)
		if body.Position == nil || body.Duration == nil || !ok ||
			*body.Position < 0 || *body.Duration < 0 {
			return invalid
		}
	}
	known, err := query(
		`SELECT 1 FROM streamline_video_files WHERE id = ?`, mediaID,
	)
	if err != nil {
		return internalError("reading a file", err)
	}
	if len(known.Rows) == 0 {
		return fail(http.StatusNotFound, "not_found", "No such media")
	}
	if offline {
		err = applyOffline(c.User.ID, mediaID, *body.Position, *body.Duration, at)
	} else {
		err = exec(
			`INSERT INTO watch(user_id, media_id, position, duration, watched,
				updated_at)
			VALUES(?, ?, 0, 0, ?, ?)
			ON CONFLICT(user_id, media_id) DO UPDATE SET
				position = 0, duration = 0, watched = excluded.watched,
				updated_at = excluded.updated_at`,
			c.User.ID, mediaID, *body.Watched, now().UnixMilli(),
		)
	}
	if err != nil {
		return internalError("saving watch state", err)
	}
	res, err := query(
		`SELECT `+watchColumns+` `+watchJoins+`
		WHERE w.user_id = ? AND w.media_id = ?`, c.User.ID, mediaID,
	)
	if err != nil {
		return internalError("reading watch state back", err)
	}
	if len(res.Rows) != 1 {
		return fail(http.StatusNotFound, "not_found", "No such media")
	}
	return jsonOK(http.StatusOK, watchItem(res.Rows[0]))
}

var offlineLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05"}

func parseOfflineTime(s string) (time.Time, bool) {
	for _, layout := range offlineLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// applyOffline keeps what a desktop app watched from a download only when it
// is newer than what the server holds.
func applyOffline(
	userID uint32,
	mediaID, position, duration int64,
	at time.Time,
) error {
	_, err := saveNewer(
		userID, mediaID, position, duration,
		duration > 0 && float64(position) >= watchedAt*float64(duration), at,
	)
	return err
}

// saveNewer reports whether the state was written: one older than what the
// server holds is dropped.
func saveNewer(
	userID uint32,
	mediaID, position, duration int64,
	watched bool,
	at time.Time,
) (bool, error) {
	res, err := run(
		`INSERT INTO watch(user_id, media_id, position, duration, watched,
			updated_at) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, media_id) DO UPDATE SET
			position = excluded.position, duration = excluded.duration,
			watched = excluded.watched, updated_at = excluded.updated_at
		WHERE excluded.updated_at > watch.updated_at`,
		userID, mediaID, position, duration, watched, at.UnixMilli(),
	)
	return res.RowsAffected > 0, err
}

type importedWatch struct {
	Email    string `json:"email"`
	TmdbID   int64  `json:"tmdb_id"`
	TvdbID   int64  `json:"tvdb_id"`
	Season   int64  `json:"season"`
	Episode  int64  `json:"episode"`
	Position int64  `json:"position_seconds"`
	Duration int64  `json:"duration_seconds"`
	Watched  bool   `json:"watched"`
	At       string `json:"at"`
}

// importWatchState takes watch state read from another server. A movie is
// named by tmdb_id, an episode by its series' tvdb_id with season and episode,
// and the state lands on every file of the title.
func importWatchState(c *call) sdk.Response {
	var body struct {
		Items []importedWatch `json:"items"`
	}
	invalid := fail(http.StatusBadRequest, "invalid_request",
		"Each item must carry email, at, and tmdb_id or tvdb_id")
	if err := decodeBody(c, &body); err != nil {
		return invalid
	}
	times := make([]time.Time, len(body.Items))
	for i, it := range body.Items {
		at, ok := parseOfflineTime(it.At)
		if !ok || it.Email == "" || (it.TmdbID > 0) == (it.TvdbID > 0) ||
			it.Position < 0 || it.Duration < 0 {
			return invalid
		}
		times[i] = at
	}

	logins, err := query(`SELECT email, user_id FROM streamline_user_logins`)
	if err != nil {
		return internalError("listing logins", err)
	}
	users := make(map[string]uint32, len(logins.Rows))
	for _, r := range logins.Rows {
		//nolint:gosec // a core user id fits
		users[row(r).str(0)] = uint32(row(r).int(1))
	}

	var imported, older, unknownTitles int
	unknownUsers := []string{}
	for i, it := range body.Items {
		email := strings.ToLower(it.Email)
		userID, ok := users[email]
		if !ok {
			if !slices.Contains(unknownUsers, email) {
				unknownUsers = append(unknownUsers, email)
			}
			continue
		}
		var files sdk.SQLResult
		if it.TmdbID > 0 {
			files, err = query(
				`SELECT f.id FROM streamline_video_files f
				JOIN streamline_movies m ON m.id = f.movie_id
				WHERE m.tmdb_id = ?`, it.TmdbID,
			)
		} else {
			files, err = query(
				`SELECT f.id FROM streamline_video_files f
				JOIN streamline_episodes e ON e.id = f.episode_id
				JOIN streamline_seasons se ON se.id = e.season_id
				JOIN streamline_series s ON s.id = se.series_id
				WHERE s.tvdb_id = ? AND se.number = ? AND e.number = ?`,
				it.TvdbID, it.Season, it.Episode,
			)
		}
		if err != nil {
			return internalError("finding a title's files", err)
		}
		if len(files.Rows) == 0 {
			unknownTitles++
			continue
		}
		watched := it.Watched || it.Duration > 0 &&
			float64(it.Position) >= watchedAt*float64(it.Duration)
		position, duration := it.Position, it.Duration
		if watched {
			position, duration = 0, 0
		}
		for _, f := range files.Rows {
			written, err := saveNewer(
				userID, row(f).int(0), position, duration, watched, times[i],
			)
			if err != nil {
				return internalError("saving imported watch state", err)
			}
			if written {
				imported++
			} else {
				older++
			}
		}
	}
	return jsonOK(http.StatusOK, map[string]any{
		"imported":       imported,
		"older":          older,
		"unknown_titles": unknownTitles,
		"unknown_users":  unknownUsers,
	})
}
