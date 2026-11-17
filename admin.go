package main

import (
	"cmp"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/datahearth/streamline/extensions/sdk"
)

const (
	maxHaltMessage = 200
	restartRetry   = 10
)

func callerLabel(u *User) string {
	if u.Name != "" {
		return u.Name
	}
	local, _, _ := strings.Cut(u.Email, "@")
	return local
}

func init() {
	handleRoute(http.MethodGet, "/admin/overview", Admin, adminOverview)
	handleRoute(http.MethodPost, "/admin/restart", Admin, adminRestart)
	handleRoute(http.MethodGet, "/admin/streams", Admin, adminStreams)
	handleRoute(
		http.MethodPost,
		"/admin/streams/{streamId}/stop",
		Admin,
		adminStopStream,
	)
	handleRoute(http.MethodGet, "/admin/devices", Admin, adminDevices)
	handleRoute(
		http.MethodDelete,
		"/admin/devices/{deviceId}",
		Admin,
		adminRevokeDevice,
	)
	handleRoute(http.MethodPost, "/admin/cache/clear", Admin, adminClearCache)
}

type engineHealth struct {
	Version         string `json:"version"`
	APIVersion      int    `json:"api_version"`
	FFmpegVersion   string `json:"ffmpeg_version"`
	AnalysisVersion int    `json:"analysis_version"`
	VAAPI           bool   `json:"vaapi"`
	Libplacebo      bool   `json:"libplacebo"`
	DoviTool        bool   `json:"dovi_tool"`
}

func adminOverview(c *call) sdk.Response {
	out := map[string]any{
		"extension":      map[string]any{"version": version},
		"streamline_url": c.R.Origin,
		"folders":        folderHealth(c),
	}
	storage := map[string]any{}
	engine := map[string]any{"reachable": false}
	var h engineHealth
	if status, err := engineDo(
		http.MethodGet, "/v1/health", nil, &h,
	); err == nil && status == http.StatusOK {
		mode := "software"
		if h.VAAPI {
			mode = "vaapi"
		}
		engine = map[string]any{
			"reachable":        true,
			"version":          h.Version,
			"api_version":      h.APIVersion,
			"ffmpeg_version":   h.FFmpegVersion,
			"analysis_version": h.AnalysisVersion,
			"hw":               map[string]any{"mode": mode, "ready": true},
			"libplacebo":       h.Libplacebo,
			"dovi_tool":        h.DoviTool,
		}
		var cache struct {
			Size    int64 `json:"size"`
			Streams int64 `json:"streams"`
		}
		if status, err := engineDo(
			http.MethodGet, "/v1/cache", nil, &cache,
		); err == nil && status == http.StatusOK {
			storage["cache"] = cache
		}
	}
	out["engine"] = engine
	out["storage"] = storage
	return jsonOK(http.StatusOK, out)
}

func folderHealth(c *call) map[string]int {
	reachable := 0
	for _, f := range libraryFolders {
		granted, ok := c.R.Folder(f.id)
		if !ok {
			continue
		}
		if guest, ok := c.R.GuestPath(granted.HostPath); ok && dirExists(guest) {
			reachable++
		}
	}
	return map[string]int{"reachable": reachable, "total": len(libraryFolders)}
}

// haltMessage reads the optional message of a stop or a restart.
func haltMessage(c *call) (string, *sdk.Response) {
	body, bad := readObject(c)
	if bad != nil {
		return "", bad
	}
	var message string
	if raw, ok := body["message"]; ok {
		if json.Unmarshal(raw, &message) != nil || len(message) > maxHaltMessage {
			resp := fail(
				http.StatusBadRequest,
				"invalid_request",
				"The message must be a string of at most 200 characters",
			)
			return "", &resp
		}
	}
	return message, nil
}

func adminRestart(c *call) sdk.Response {
	message, bad := haltMessage(c)
	if bad != nil {
		return *bad
	}
	status, raw, err := engineFetch(http.MethodPost, "/v1/restart", nil)
	if err != nil {
		return engineUnavailable()
	}
	if status/100 != 2 {
		return engineError(status, raw)
	}
	until := now().Add(restartRetry * time.Second).Unix()
	by := callerLabel(c.User)
	if err := noteRestart(restartWindow{until, by, message}); err != nil {
		return internalError("noting the restart", err)
	}
	if err := exec(
		`UPDATE sessions SET stopped_code = ?, stopped_message = ?, stopped_by = ?,
			retry_until = ? WHERE COALESCE(stopped_code, '') = ''`,
		haltRestarting, message, by, until,
	); err != nil {
		return internalError("noting the restart on the sessions", err)
	}
	return sdk.Response{Status: http.StatusAccepted}
}

var engineModes = map[string]string{
	"direct":          "copy",
	"remux":           "copy",
	"audio_transcode": "audio",
	"transcode":       "transcode",
}

type engineSession struct {
	SessionID string `json:"session_id"`
	Mode      string `json:"mode"`
	HW        string `json:"hw"`
	Video     struct{ Codec string }
	Audio     struct{ Codec string }
	Source    struct{ Video, Audio string }
	Speed     float64 `json:"speed"`
	Bitrate   int64   `json:"bitrate"`
}

// engineSessions is what the engine says of its live sessions; an engine that
// does not answer says nothing.
func engineSessions() map[string]engineSession {
	var list struct {
		Sessions []engineSession `json:"sessions"`
	}
	infos := map[string]engineSession{}
	if status, err := engineDo(
		http.MethodGet, "/v1/sessions", nil, &list,
	); err != nil || status != http.StatusOK {
		return infos
	}
	for _, s := range list.Sessions {
		infos[s.SessionID] = s
	}
	return infos
}

func deviceKindOf(kind, platform string) string {
	p := strings.ToLower(platform)
	switch {
	case kind == kindBrowser:
		return "browser"
	case strings.Contains(p, "tv"):
		return "tv"
	case strings.Contains(p, "ipad") || strings.Contains(p, "tablet"):
		return "tablet"
	case strings.Contains(p, "android") || strings.Contains(p, "ios") ||
		strings.Contains(p, "iphone"):
		return "phone"
	}
	return "desktop"
}

func adminStreams(c *call) sdk.Response {
	res, err := query(
		`SELECT s.id, s.user_id, `+userLabel+`,
			COALESCE(d.name, ''), COALESCE(d.kind, ''), COALESCE(d.platform, ''),
			COALESCE(d.last_ip, ''), s.position, s.paused,
			COALESCE(f.duration, 0), f.movie_id, se.series_id, se.number, e.number
		FROM sessions s
		LEFT JOIN streamline_users u ON u.id = s.user_id
		LEFT JOIN devices d ON d.id = s.device_id
		LEFT JOIN streamline_video_files f ON f.id = s.media_id
		LEFT JOIN streamline_episodes e ON e.id = f.episode_id
		LEFT JOIN streamline_seasons se ON se.id = e.season_id
		WHERE s.last_beat > ? AND COALESCE(s.stopped_code, '') = ''
		ORDER BY s.started_at, s.id`,
		now().Unix()-heartbeatSilence,
	)
	if err != nil {
		return internalError("listing streams", err)
	}
	infos := engineSessions()
	items := make([]map[string]any, 0, len(res.Rows))
	for _, raw := range res.Rows {
		r := row(raw)
		item := streamItem(r, infos[r.str(0)], c.User.ID)
		if item != nil {
			items = append(items, item)
		}
	}
	return jsonOK(http.StatusOK, map[string]any{"items": items})
}

func streamItem(r row, info engineSession, caller uint32) map[string]any {
	var title map[string]any
	var season, episode any
	switch {
	case r[10] != nil:
		title = map[string]any{"kind": "movies", "id": r.int(10)}
	case r[11] != nil:
		title = map[string]any{"kind": "series", "id": r.int(11)}
		season, episode = r.int(12), r.int(13)
	default:
		return nil
	}
	network := "remote"
	if networkOf(r.str(6)) == "home" {
		network = "home"
	}
	mode := engineModes[info.Mode]
	if mode == "" {
		mode = "copy"
	}
	item := map[string]any{
		"id": r.str(0),
		"user": map[string]any{
			"name": r.str(2),
			"you":  r.int(1) == int64(caller),
		},
		"title": title,
		"device": map[string]any{
			"name": r.str(3),
			"kind": deviceKindOf(r.str(4), r.str(5)),
		},
		"network":  network,
		"position": libFloat(r[7]),
		"duration": libFloat(r[9]),
		"playing":  r.int(8) == 0,
		"mode":     mode,
		"bitrate":  info.Bitrate,
	}
	if season != nil {
		item["season"], item["episode"] = season, episode
	}
	switch mode {
	case "copy":
		item["video"], item["audio"] = info.Video.Codec, info.Audio.Codec
	case "audio":
		item["from"], item["to"] = info.Source.Audio, info.Audio.Codec
	default:
		item["from"], item["to"] = info.Source.Video, info.Video.Codec
		item["hw"] = cmp.Or(info.HW, "software")
		if info.Speed > 0 {
			item["speed"] = info.Speed
		}
	}
	return item
}

func adminStopStream(c *call) sdk.Response {
	id := c.Params["streamId"]
	if !sessionIDPattern.MatchString(id) {
		return fail(http.StatusNotFound, "not_found", "No such stream")
	}
	message, bad := haltMessage(c)
	if bad != nil {
		return *bad
	}
	out, err := run(
		`UPDATE sessions SET stopped_code = ?, stopped_message = ?, stopped_by = ?
		WHERE id = ? AND COALESCE(stopped_code, '') = ''`,
		haltStopped, message, callerLabel(c.User), id,
	)
	if err != nil {
		return internalError("stopping a stream", err)
	}
	if out.RowsAffected == 0 {
		return fail(http.StatusNotFound, "not_found", "No such stream")
	}
	return sdk.Response{Status: http.StatusNoContent}
}

func adminDevices(c *call) sdk.Response {
	res, err := query(
		`SELECT `+deviceColumns+`, `+userLabel+`
		FROM devices d LEFT JOIN streamline_users u ON u.id = d.user_id
		WHERE d.kind = 'app' OR EXISTS(SELECT 1 FROM refresh_tokens t
			WHERE t.device_id = d.id AND t.rotated = 0 AND t.expires_at > ?)`,
		now().Unix(),
	)
	if err != nil {
		return internalError("listing devices", err)
	}
	streaming, err := streamingDeviceIDs()
	if err != nil {
		logFailure("reading live sessions", err)
	}
	rows := make([]listedDevice, 0, len(res.Rows))
	for _, raw := range res.Rows {
		r := row(raw)
		id := r.int(0)
		item := map[string]any{
			"id":        strconv.FormatInt(id, 10),
			"kind":      r.str(1),
			"name":      r.str(2),
			"platform":  r.str(3),
			"last_seen": isoTime(r.int(4)),
			"streaming": streaming[id],
			"current":   id == c.Device.ID,
			"user":      map[string]any{"name": r.str(6)},
		}
		if r.str(1) == kindBrowser {
			if item["platform"] == "" {
				item["platform"] = "Web browser"
			}
			item["expires_at"] = isoTime(r.int(5))
		}
		rows = append(rows, listedDevice{id, r.int(4), item})
	}
	sortDevices(rows)
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		items = append(items, r.item)
	}
	return jsonOK(http.StatusOK, map[string]any{"items": items})
}

// streamingDeviceIDs is every device with a live session, whoever owns it.
func streamingDeviceIDs() (map[int64]bool, error) {
	res, err := query(
		`SELECT DISTINCT device_id FROM sessions
		WHERE last_beat > ? AND COALESCE(stopped_code, '') = ''`,
		now().Unix()-heartbeatSilence,
	)
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, r := range res.Rows {
		out[row(r).int(0)] = true
	}
	return out, nil
}

func adminRevokeDevice(c *call) sdk.Response {
	id, err := strconv.ParseInt(c.Params["deviceId"], 10, 64)
	if err != nil {
		return fail(http.StatusNotFound, "not_found", "No such device")
	}
	if id == c.Device.ID {
		return fail(
			http.StatusConflict,
			"current_device",
			"This is the device making the request",
		)
	}
	res, err := query(`SELECT 1 FROM devices WHERE id = ?`, id)
	if err != nil {
		return internalError("reading a device", err)
	}
	if len(res.Rows) == 0 {
		return fail(http.StatusNotFound, "not_found", "No such device")
	}
	if err := revokeDevice(id); err != nil {
		return internalError("revoking the device", err)
	}
	return sdk.Response{Status: http.StatusNoContent}
}

func adminClearCache(*call) sdk.Response {
	var out struct {
		Freed int64 `json:"freed"`
	}
	status, raw, err := engineFetch(http.MethodPost, "/v1/cache/clear", nil)
	if err != nil {
		return engineUnavailable()
	}
	if status/100 != 2 {
		return engineError(status, raw)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return engineUnavailable()
	}
	return jsonOK(http.StatusOK, out)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
