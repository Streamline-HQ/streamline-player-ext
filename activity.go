package main

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/scan"
)

const (
	activityLimit = 100
	idPrefix      = "a"
	// Episodes that arrive within this window of the last episodes event for
	// the same season join it, so a season landing file by file is one entry.
	mergeWindow = 10 * time.Minute

	kindEpisodes = "episodes"
)

func init() {
	registerSchema(
		`CREATE TABLE IF NOT EXISTS activity(
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			kind TEXT NOT NULL,
			at INTEGER NOT NULL,
			device_id INTEGER,
			body TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS activity_at ON activity(at, seq)`,
		// The feed no longer has removals; the ones recorded before would
		// reach apps that do not know the kind.
		`DELETE FROM activity WHERE kind = 'removed'`,
		`CREATE TABLE IF NOT EXISTS activity_reads(
			user_id INTEGER NOT NULL,
			seq INTEGER NOT NULL,
			PRIMARY KEY(user_id, seq)
		) WITHOUT ROWID`,
	)
	handleRoute(http.MethodGet, "/activity", Any, listActivity)
	handleRoute(http.MethodPost, "/activity/read", Any, markActivityRead)
	addEvent = recordLibraryEvent
}

func listActivity(c *call) sdk.Response {
	res, err := query(
		`SELECT a.seq, a.kind, a.at, a.body, r.seq IS NOT NULL
		FROM activity a
		LEFT JOIN activity_reads r ON r.seq = a.seq AND r.user_id = ?
		WHERE a.device_id IS NULL OR a.device_id = ?
		ORDER BY a.at DESC, a.seq DESC LIMIT ?`,
		c.User.ID, c.Device.ID, activityLimit,
	)
	if err != nil {
		return internalError("listing activity", err)
	}
	items := make([]map[string]any, 0, len(res.Rows))
	for _, r := range res.Rows {
		r := row(r)
		item := map[string]any{}
		if err := json.Unmarshal([]byte(r.str(3)), &item); err != nil {
			return internalError("reading an activity event", err)
		}
		item["id"] = idPrefix + strconv.FormatInt(r.int(0), 10)
		item["kind"] = r.str(1)
		item["at"] = time.Unix(r.int(2), 0).UTC().Format(time.RFC3339)
		item["read"] = r[4] == true || r.int(4) == 1
		items = append(items, item)
	}
	return jsonOK(http.StatusOK, map[string]any{"items": items})
}

func markActivityRead(c *call) sdk.Response {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeBody(c, &body); err != nil || len(body.IDs) == 0 ||
		len(body.IDs) > activityLimit {
		return fail(http.StatusBadRequest, "invalid_request",
			"ids must hold between 1 and 100 event ids")
	}
	seqs := make([]any, 0, len(body.IDs))
	for _, id := range body.IDs {
		if n, err := strconv.ParseInt(
			strings.TrimPrefix(id, idPrefix),
			10,
			64,
		); err == nil &&
			strings.HasPrefix(id, idPrefix) {
			seqs = append(seqs, n)
		}
	}
	if len(seqs) == 0 {
		return sdk.Response{Status: http.StatusNoContent}
	}
	args := append([]any{c.User.ID, c.Device.ID}, seqs...)
	if err := exec(
		`INSERT OR IGNORE INTO activity_reads(user_id, seq)
		SELECT ?1, a.seq FROM activity a
		WHERE (a.device_id IS NULL OR a.device_id = ?2)
			AND a.seq IN (`+placeholders(len(seqs))+`)`,
		args...,
	); err != nil {
		return internalError("marking activity read", err)
	}
	return sdk.Response{Status: http.StatusNoContent}
}

func insertEvent(kind string, deviceID any, body map[string]any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return exec(
		`INSERT INTO activity(kind, at, device_id, body) VALUES(?, ?, ?, ?)`,
		kind, now().Unix(), deviceID, string(raw),
	)
}

// episodeRef is where an episode sits: its series, season and number.
func episodeRef(episodeID int64) (series, season, number int64, ok bool) {
	res, err := query(
		`SELECT se.series_id, se.number, e.number FROM streamline_episodes e
		JOIN streamline_seasons se ON se.id = e.season_id WHERE e.id = ?`,
		episodeID,
	)
	if err != nil || len(res.Rows) == 0 {
		return 0, 0, 0, false
	}
	r := row(res.Rows[0])
	return r.int(0), r.int(1), r.int(2), true
}

// titleRef is the title a file belongs to, with the episode's place when it is
// one. ok is false for a file no title owns.
func titleRef(movieID, episodeID int64) (title, extra map[string]any, ok bool) {
	if movieID != 0 {
		return map[string]any{"kind": "movies", "id": movieID}, nil, true
	}
	series, season, number, found := episodeRef(episodeID)
	if !found {
		return nil, nil, false
	}
	return map[string]any{"kind": "series", "id": series},
		map[string]any{"season": season, "episodes": []int64{number}}, true
}

const qualityColumns = `quality, height, video_codec, dynamic_range, audio_codec,
	channels`

// qualitySummary is a file as the feed describes it, from its size and a row
// of qualityColumns.
func qualitySummary(size int64, r row) map[string]any {
	out := map[string]any{"size": size}
	for key, v := range map[string]string{
		"resolution": libResolution(r.str(0), r.int(1)),
		"codec":      r.str(2),
		"hdr":        libHDR(r.str(3)),
		"audio":      r.str(4),
		"channels":   libChannels(r.int(5)),
	} {
		if v != "" {
			out[key] = v
		}
	}
	return out
}

func fileSummaryOf(fileID int64) map[string]any {
	res, err := query(
		`SELECT size, `+qualityColumns+` FROM streamline_video_files
		WHERE id = ?`, fileID,
	)
	if err != nil || len(res.Rows) == 0 {
		return map[string]any{}
	}
	r := row(res.Rows[0])
	return qualitySummary(r.int(0), r[1:])
}

// previousSummaryOf describes a file that is no longer in the library, or no
// longer the same file, from what the last comparison kept of it.
func previousSummaryOf(p *scan.Seen) map[string]any {
	res, err := query(
		`SELECT `+qualityColumns+` FROM seen_quality WHERE video_file_id = ?`,
		p.ID,
	)
	if err != nil || len(res.Rows) == 0 {
		return map[string]any{"size": p.Size}
	}
	return qualitySummary(p.Size, row(res.Rows[0]))
}

// recordLibraryEvent turns what a comparison found into the feed's entries: a
// file that arrived, or one that replaced another.
func recordLibraryEvent(e libraryEvent) {
	title, place, ok := titleRef(e.File.MovieID, e.File.EpisodeID)
	if !ok {
		return
	}
	var err error
	switch {
	case e.Replaced:
		body := map[string]any{
			"title": title, "file": fileSummaryOf(e.File.ID),
			"previous": map[string]any{},
		}
		if e.Previous != nil {
			body["previous"] = previousSummaryOf(e.Previous)
		}
		maps.Copy(body, place)
		err = insertEvent("upgrade", nil, body)
	case e.File.MovieID != 0:
		err = insertEvent("movie", nil, map[string]any{
			"title": title, "file": fileSummaryOf(e.File.ID),
		})
	default:
		err = addEpisode(title, place)
	}
	if err != nil {
		logFailure("recording an activity event", err)
	}
}

// addEpisode adds one episode to the last episodes event of its season when
// that is recent, and starts a new event otherwise.
func addEpisode(title, place map[string]any) error {
	res, err := query(
		`SELECT seq, at, body FROM activity
		WHERE kind = ? AND device_id IS NULL ORDER BY seq DESC LIMIT 1`,
		kindEpisodes,
	)
	if err != nil {
		return err
	}
	if len(res.Rows) == 1 {
		r := row(res.Rows[0])
		var held struct {
			Title    map[string]any `json:"title"`
			Season   int64          `json:"season"`
			Episodes []int64        `json:"episodes"`
		}
		if json.Unmarshal([]byte(r.str(2)), &held) == nil &&
			now().Unix()-r.int(1) <= int64(mergeWindow.Seconds()) &&
			num(held.Title["id"]) == num(title["id"]) &&
			held.Season == place["season"] {
			number := place["episodes"].([]int64)[0]
			if !slices.Contains(held.Episodes, number) {
				held.Episodes = slices.Sorted(
					slices.Values(append(held.Episodes, number)),
				)
			}
			raw, err := json.Marshal(map[string]any{
				"title": title, "season": held.Season, "episodes": held.Episodes,
			})
			if err != nil {
				return err
			}
			if err := exec(
				`UPDATE activity SET body = ?, at = ? WHERE seq = ?`,
				string(raw), now().Unix(), r.int(0),
			); err != nil {
				return err
			}
			return exec(`DELETE FROM activity_reads WHERE seq = ?`, r.int(0))
		}
	}
	body := map[string]any{"title": title}
	maps.Copy(body, place)
	return insertEvent(kindEpisodes, nil, body)
}
