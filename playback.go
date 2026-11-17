package main

import (
	"crypto/md5" //nolint:gosec // the engine's job ids are md5 of the file, not a secret
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/sign"
)

const (
	hlsTTL   = 6 * time.Hour
	mediaTTL = 12 * time.Hour

	analysisWait = 20 * time.Second
	jobPoll      = 250 * time.Millisecond
)

var sessionIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// analysisSource and markersSource are where the library comparison's store
// plugs in. Until it does, a title is sent without its analysis and the engine
// probes it itself.
var (
	analysisSource = func(string) (json.RawMessage, bool) { return nil, false }
	markersSource  = func(int64) (intro, credits *[2]float64) { return nil, nil }
)

func init() {
	registerSchema(
		`CREATE TABLE IF NOT EXISTS sessions(
			id TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			device_id INTEGER NOT NULL,
			media_id INTEGER NOT NULL,
			path TEXT NOT NULL,
			started_at INTEGER NOT NULL,
			last_beat INTEGER NOT NULL,
			position REAL NOT NULL DEFAULT 0,
			paused INTEGER NOT NULL DEFAULT 0,
			url_path TEXT NOT NULL DEFAULT '',
			stopped_code TEXT NOT NULL DEFAULT '',
			stopped_message TEXT NOT NULL DEFAULT '',
			stopped_by TEXT NOT NULL DEFAULT '',
			retry_until INTEGER NOT NULL DEFAULT 0
		) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS sessions_beat ON sessions(last_beat)`,
		`CREATE INDEX IF NOT EXISTS sessions_device ON sessions(device_id)`,
	)
	handleRoute(http.MethodGet, "/playback/status", Any, playbackStatus)
	handleRoute(http.MethodPost, "/playback/decisions", Any, playbackDecisions)
	handleRoute(http.MethodPost, "/playback/sessions", Any, startSession)
	handleRoute(http.MethodGet, "/playback/sessions/{sessionId}", Any, getSession)
	handleRoute(
		http.MethodPatch,
		"/playback/sessions/{sessionId}",
		Any,
		patchSession,
	)
	handleRoute(
		http.MethodDelete,
		"/playback/sessions/{sessionId}",
		Any,
		stopSession,
	)
	handleRoute(
		http.MethodPost,
		"/playback/sessions/{sessionId}/progress",
		Any,
		heartbeat,
	)
	handleRoute(http.MethodGet, "/playback/media/{mediaId}/tracks", Any, mediaTracks)
}

func logFailure(what string, err error) {
	sdk.Log("warn", what+" failed", "error", err.Error())
}

type playFile struct {
	ID       int64
	Path     string
	Duration float64
}

// num reads a number out of a decoded JSON document or out of a map built
// here, where ids are integers.
func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	}
	return 0
}

// lookupFile finds the video file a player named and checks it is still on
// disk.
func lookupFile(c *call, id string) (playFile, *sdk.Response) {
	notFound := func(code, message string) (playFile, *sdk.Response) {
		resp := fail(http.StatusNotFound, code, message)
		return playFile{}, &resp
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return notFound("media_not_found", "No such media")
	}
	res, err := query(
		`SELECT path, duration, missing FROM streamline_video_files WHERE id = ?`, n,
	)
	if err != nil {
		resp := internalError("reading a file", err)
		return playFile{}, &resp
	}
	if len(res.Rows) == 0 {
		return notFound("media_not_found", "No such media")
	}
	r := row(res.Rows[0])
	f := playFile{ID: n, Path: r.str(0), Duration: num(r[1])}
	if r.int(2) != 0 {
		return notFound("file_not_found", "The file is gone from disk")
	}
	if guest, ok := c.R.GuestPath(f.Path); !ok || !fileExists(guest) {
		return notFound("file_not_found", "The file is gone from disk")
	}
	return f, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// pick copies the named keys of body, so nothing a player sends reaches the
// engine unasked, a path least of all.
func pick(body map[string]json.RawMessage, names ...string) map[string]any {
	out := map[string]any{}
	for _, n := range names {
		if v, ok := body[n]; ok {
			out[n] = v
		}
	}
	return out
}

func readObject(c *call) (map[string]json.RawMessage, *sdk.Response) {
	body := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(c.R.Body))) > 0 {
		if err := decodeBody(c, &body); err != nil || body == nil {
			resp := fail(
				http.StatusBadRequest,
				"invalid_request",
				"The body must be a JSON object",
			)
			return nil, &resp
		}
	}
	return body, nil
}

func mediaIDOf(body map[string]json.RawMessage) string {
	raw := body["media_id"]
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

func playbackStatus(c *call) sdk.Response {
	status, err := engineDo(http.MethodGet, "/v1/health", nil, nil)
	return jsonOK(http.StatusOK, map[string]any{
		"available": err == nil && status == http.StatusOK,
		"network":   networkOf(c.R.ClientIP),
	})
}

func networkOf(clientIP string) string {
	addr, err := netip.ParseAddr(clientIP)
	if err != nil {
		return "away"
	}
	addr = addr.Unmap()
	for _, cidr := range settingStrings("network.home") {
		if p, err := netip.ParsePrefix(cidr); err == nil && p.Contains(addr) {
			return "home"
		}
	}
	return "away"
}

func playbackDecisions(c *call) sdk.Response {
	body, bad := readObject(c)
	if bad != nil {
		return *bad
	}
	f, missing := lookupFile(c, mediaIDOf(body))
	if missing != nil {
		return *missing
	}
	req := pick(body, "capabilities", "options")
	req["path"] = f.Path
	if a, ok := analysisSource(f.Path); ok {
		req["analysis"] = a
	}
	status, raw, err := engineFetch(http.MethodPost, "/v1/decisions", req)
	if err != nil {
		return engineUnavailable()
	}
	if status/100 != 2 {
		return engineError(status, raw)
	}
	return jsonResponse(http.StatusOK, raw)
}

// engineSigner is what minting a URL needs, or nothing when the engine is not
// set up.
type engineSigner struct{ public, secret string }

func newSigner() (engineSigner, bool) {
	public, ok := publicEngineURL()
	secret, hasSecret := engineSecret()
	return engineSigner{public, secret}, ok && hasSecret
}

func (e engineSigner) signed(scope, id, path string, ttl time.Duration) string {
	exp := now().Add(ttl).Unix()
	return fmt.Sprintf(
		"%s/%s?exp=%d&sig=%s",
		e.public,
		path,
		exp,
		sign.Sign(e.secret, scope, id, exp),
	)
}

func (e engineSigner) session(id, urlPath string) string {
	if urlPath == "file" {
		return e.signed("hls", id, "file/"+id, hlsTTL)
	}
	return e.signed("hls", id, "hls/"+id+"/master.m3u8", hlsTTL)
}

func (e engineSigner) media(job, name string) string {
	return e.signed("media", job, "media/"+job+"/"+sign.EscapePath(name), mediaTTL)
}

// withURL is the engine's answer to a start or a patch plus what the player
// needs to fetch the stream.
func withURL(raw []byte, id string, s engineSigner) (map[string]any, string, error) {
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, "", err
	}
	urlPath, _ := out["url_path"].(string)
	out["session_id"] = id
	out["url"] = s.session(id, urlPath)
	return out, urlPath, nil
}

func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func startSession(c *call) sdk.Response {
	if resp := restartAnswer(); resp != nil {
		return *resp
	}
	body, bad := readObject(c)
	if bad != nil {
		return *bad
	}
	f, missing := lookupFile(c, mediaIDOf(body))
	if missing != nil {
		return *missing
	}
	signer, ok := newSigner()
	if !ok {
		return engineUnavailable()
	}
	id, err := newSessionID()
	if err != nil {
		return internalError("minting a session id", err)
	}
	req := pick(
		body,
		"capabilities",
		"position",
		"audio_index",
		"burn_in_subtitle",
		"max_bitrate",
	)
	req["session_id"] = id
	req["path"] = f.Path
	if a, ok := analysisSource(f.Path); ok {
		req["analysis"] = a
	}
	status, raw, err := engineFetch(http.MethodPost, "/v1/sessions", req)
	if err != nil {
		return engineUnavailable()
	}
	if status/100 != 2 {
		return engineError(status, raw)
	}
	out, urlPath, err := withURL(raw, id, signer)
	if err != nil {
		endSession(id)
		return engineUnavailable()
	}
	at := now().Unix()
	position, _ := req["position"].(json.RawMessage)
	var start float64
	_ = json.Unmarshal(position, &start)
	if err := exec(
		`INSERT INTO sessions(id, user_id, device_id, media_id, path, started_at,
			last_beat, position, url_path) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, c.User.ID, c.Device.ID, f.ID, f.Path, at, at, start, urlPath,
	); err != nil {
		endSession(id)
		return internalError("saving a session", err)
	}
	return jsonOK(http.StatusOK, out)
}

type sessionRow struct {
	id      string
	mediaID int64
	halt    halt
}

// ownSession loads the caller's session and applies any halt left on it.
func ownSession(c *call) (sessionRow, *sdk.Response) {
	id := c.Params["sessionId"]
	notFound := func() (sessionRow, *sdk.Response) {
		resp := fail(http.StatusNotFound, "not_found", "No such session")
		return sessionRow{}, &resp
	}
	if !sessionIDPattern.MatchString(id) {
		return notFound()
	}
	res, err := query(
		`SELECT media_id, stopped_code, stopped_message, stopped_by, retry_until
		FROM sessions WHERE id = ? AND user_id = ?`, id, c.User.ID,
	)
	if err != nil {
		resp := internalError("reading a session", err)
		return sessionRow{}, &resp
	}
	if len(res.Rows) == 0 {
		return notFound()
	}
	r := row(res.Rows[0])
	s := sessionRow{id: id, mediaID: r.int(0), halt: halt{
		code: r.str(1), message: r.str(2), by: r.str(3), retryUntil: r.int(4),
	}}
	if s.halt.code != "" {
		if resp := s.halt.answer(id); resp != nil {
			return sessionRow{}, resp
		}
	}
	return s, nil
}

func getSession(c *call) sdk.Response {
	s, bad := ownSession(c)
	if bad != nil {
		return *bad
	}
	status, raw, err := engineFetch(http.MethodGet, "/v1/sessions", nil)
	if err != nil {
		return engineUnavailable()
	}
	if status/100 != 2 {
		return engineError(status, raw)
	}
	var list struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return engineUnavailable()
	}
	for _, item := range list.Sessions {
		var head struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(item, &head) == nil && head.SessionID == s.id {
			return jsonResponse(http.StatusOK, item)
		}
	}
	return fail(http.StatusNotFound, "not_found", "No such session")
}

func patchSession(c *call) sdk.Response {
	s, bad := ownSession(c)
	if bad != nil {
		return *bad
	}
	body, bad := readObject(c)
	if bad != nil {
		return *bad
	}
	signer, ok := newSigner()
	if !ok {
		return engineUnavailable()
	}
	req := pick(body, "audio_index", "burn_in_subtitle", "max_bitrate", "position")
	status, raw, err := engineFetch(http.MethodPatch, "/v1/sessions/"+s.id, req)
	if err != nil {
		return engineUnavailable()
	}
	if status/100 != 2 {
		return engineError(status, raw)
	}
	out, urlPath, err := withURL(raw, s.id, signer)
	if err != nil {
		return engineUnavailable()
	}
	if err := exec(
		`UPDATE sessions SET url_path = ? WHERE id = ?`,
		urlPath,
		s.id,
	); err != nil {
		logFailure("saving a session", err)
	}
	return jsonOK(http.StatusOK, out)
}

func stopSession(c *call) sdk.Response {
	id := c.Params["sessionId"]
	if !sessionIDPattern.MatchString(id) {
		return fail(http.StatusNotFound, "not_found", "No such session")
	}
	res, err := query(`SELECT user_id FROM sessions WHERE id = ?`, id)
	if err != nil {
		return internalError("reading a session", err)
	}
	if len(res.Rows) > 0 && row(res.Rows[0]).int(0) != int64(c.User.ID) {
		return fail(http.StatusNotFound, "not_found", "No such session")
	}
	endSession(id)
	return sdk.Response{Status: http.StatusNoContent}
}

func heartbeat(c *call) sdk.Response {
	s, bad := ownSession(c)
	if bad != nil {
		return *bad
	}
	var body struct {
		Position *float64 `json:"position"`
		Paused   bool     `json:"paused"`
	}
	if err := decodeBody(c, &body); err != nil || body.Position == nil ||
		*body.Position < 0 || math.IsNaN(*body.Position) || math.IsInf(*body.Position, 0) {
		return fail(http.StatusBadRequest, "invalid_request",
			"The body must carry a position and paused")
	}
	at := now()
	if err := exec(
		`UPDATE sessions SET last_beat = ?, position = ?, paused = ? WHERE id = ?`,
		at.Unix(), *body.Position, body.Paused, s.id,
	); err != nil {
		return internalError("recording a heartbeat", err)
	}
	if err := exec(
		`INSERT INTO watch(user_id, media_id, position, duration, watched, updated_at)
		SELECT ?, f.id, ?, f.duration, f.duration > 0 AND ? >= ? * f.duration, ?
		FROM streamline_video_files f WHERE f.id = ?
		ON CONFLICT(user_id, media_id) DO UPDATE SET
			position = excluded.position, duration = excluded.duration,
			watched = excluded.watched, updated_at = excluded.updated_at`,
		c.User.ID,
		*body.Position,
		*body.Position,
		watchedAt,
		at.UnixMilli(),
		s.mediaID,
	); err != nil {
		return internalError("saving watch state", err)
	}
	return sdk.Response{Status: http.StatusNoContent}
}

type jobStatus struct {
	State  string          `json:"state"`
	Result json.RawMessage `json:"result"`
}

// playJobID is the engine's job id for a file: it changes when the file does.
func playJobID(kind, hostPath, guestPath string) (string, error) {
	info, err := os.Stat(guestPath)
	if err != nil {
		return "", err
	}
	sum := md5.Sum( //nolint:gosec // see the import
		fmt.Appendf(
			nil,
			"%s|%s|%d|%d",
			kind,
			hostPath,
			info.Size(),
			info.ModTime().UnixNano(),
		),
	)
	return hex.EncodeToString(sum[:]), nil
}

// jobState reads a job from the engine and submits it when the engine has
// never seen it. err is set only when the engine did not answer.
func jobState(id, kind, hostPath, priority string) (jobStatus, error) {
	status, raw, err := engineFetch(http.MethodGet, "/v1/jobs/"+id, nil)
	if err != nil {
		return jobStatus{}, err
	}
	failed := jobStatus{State: "failed"}
	switch {
	case status == http.StatusNotFound:
		submitted, _, err := engineFetch(http.MethodPost, "/v1/jobs", map[string]any{
			"job_id": id, "type": kind, "priority": priority, "path": hostPath,
		})
		if err != nil {
			return jobStatus{}, err
		}
		if submitted/100 != 2 && submitted != http.StatusConflict {
			return failed, nil
		}
		return jobStatus{State: "queued"}, nil
	case status != http.StatusOK:
		return failed, nil
	}
	var js jobStatus
	if json.Unmarshal(raw, &js) != nil {
		return failed, nil
	}
	return js, nil
}

// analysisOf returns what the engine knows of the file: the stored analysis,
// or, with none stored, the output of an analyze job waited on for a while.
func analysisOf(f playFile, guest string) (json.RawMessage, error) {
	if a, ok := analysisSource(f.Path); ok {
		return a, nil
	}
	id, err := playJobID("analyze", f.Path, guest)
	if err != nil {
		return nil, nil //nolint:nilerr // a file that cannot be read has no analysis
	}
	// A scan may hold this job in the engine's background queue, behind
	// thousands of others. Asking for it as interactive moves it to the front;
	// for a job the engine does not have, or already runs, this changes nothing.
	if _, _, err := engineFetch(http.MethodPost, "/v1/jobs", map[string]any{
		"job_id": id, "type": "analyze", "priority": "interactive", "path": f.Path,
	}); err != nil {
		return nil, err
	}
	deadline := now().Add(analysisWait)
	for {
		js, err := jobState(id, "analyze", f.Path, "interactive")
		if err != nil {
			return nil, err
		}
		switch js.State {
		case "done":
			return js.Result, nil
		case "failed":
			return nil, nil
		}
		if !now().Before(deadline) {
			return nil, nil
		}
		time.Sleep(jobPoll)
	}
}

type subtitleJob struct {
	Tracks []struct {
		Index int      `json:"index"`
		Files []string `json:"files"`
	} `json:"tracks"`
	Fonts []string `json:"fonts"`
}

func mediaTracks(c *call) sdk.Response {
	f, missing := lookupFile(c, c.Params["mediaId"])
	if missing != nil {
		return *missing
	}
	signer, ok := newSigner()
	if !ok {
		return engineUnavailable()
	}
	guest, _ := c.R.GuestPath(f.Path)
	analysis, err := analysisOf(f, guest)
	if err != nil {
		return engineUnavailable()
	}
	var a struct {
		Container string            `json:"container"`
		Video     []json.RawMessage `json:"video"`
		Chapters  []json.RawMessage `json:"chapters"`
		Audio     []json.RawMessage `json:"audio"`
		Subtitles []map[string]any  `json:"subtitles"`
	}
	if len(analysis) > 0 {
		_ = json.Unmarshal(analysis, &a)
	}

	subs, subsJob, err := sideJob("subtitles", f, guest)
	if err != nil {
		return engineUnavailable()
	}
	var side subtitleJob
	if subs.State == "done" {
		_ = json.Unmarshal(subs.Result, &side)
	}
	files := map[int]map[string]string{}
	for _, t := range side.Tracks {
		byFormat := map[string]string{}
		for _, name := range t.Files {
			if _, format, ok := strings.CutLast(name, "."); ok {
				byFormat[format] = signer.media(subsJob, name)
			}
		}
		files[t.Index] = byFormat
	}
	subtitles := make([]map[string]any, 0, len(a.Subtitles))
	for _, s := range a.Subtitles {
		byFormat := files[int(num(s["index"]))]
		if byFormat == nil {
			byFormat = map[string]string{}
		}
		s["files"] = byFormat
		subtitles = append(subtitles, s)
	}
	fonts := make([]string, 0, len(side.Fonts))
	for _, name := range side.Fonts {
		fonts = append(fonts, signer.media(subsJob, name))
	}

	var trickplay any
	if tp, tpJob, err := sideJob(
		"trickplay",
		f,
		guest,
	); err == nil &&
		tp.State == "done" {
		trickplay = map[string]string{"url": signer.media(tpJob, "trickplay.json")}
	}

	var video any
	if len(a.Video) > 0 {
		video = a.Video[0]
	}
	intro, credits := markersSource(f.ID)
	return jsonOK(http.StatusOK, map[string]any{
		"preparing": subs.State != "done",
		"container": a.Container,
		"video":     video,
		"chapters":  nonNil(a.Chapters),
		"markers":   map[string]any{"intro": intro, "credits": credits},
		"audio":     nonNil(a.Audio),
		"subtitles": subtitles,
		"fonts":     fonts,
		"trickplay": trickplay,
	})
}

func nonNil(v []json.RawMessage) []json.RawMessage {
	if v == nil {
		return []json.RawMessage{}
	}
	return v
}

// sideJob is a background job over one file whose output the player fetches
// from the engine: subtitles and trickplay sheets.
func sideJob(kind string, f playFile, guest string) (jobStatus, string, error) {
	id, err := playJobID(kind, f.Path, guest)
	if err != nil {
		return jobStatus{State: "failed"}, "", nil //nolint:nilerr // see analysisOf
	}
	js, err := jobState(id, kind, f.Path, "background")
	return js, id, err
}
