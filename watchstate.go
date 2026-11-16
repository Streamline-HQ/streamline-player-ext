package main

import (
	"net/http"
	"strconv"
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
		Watched *bool `json:"watched"`
	}
	if err := decodeBody(c, &body); err != nil || body.Watched == nil {
		return fail(http.StatusBadRequest, "invalid_request",
			"The body must carry watched")
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
	if err := exec(
		`INSERT INTO watch(user_id, media_id, position, duration, watched,
			updated_at)
		VALUES(?, ?, 0, 0, ?, ?)
		ON CONFLICT(user_id, media_id) DO UPDATE SET
			position = 0, duration = 0, watched = excluded.watched,
			updated_at = excluded.updated_at`,
		c.User.ID, mediaID, *body.Watched, now().UnixMilli(),
	); err != nil {
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
