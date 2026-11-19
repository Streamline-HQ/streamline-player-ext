package main

import (
	"bytes"
	"crypto/md5" //nolint:gosec // the engine's job ids are md5, not a secret
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"

	"github.com/datahearth/streamline/extensions/sdk"
)

// downloadCaps is what each quality asks of the engine's download job on top
// of the client's capabilities; original adds nothing.
var downloadCaps = map[string]map[string]any{
	"original": {},
	"1080p":    {"max_bitrate": 8_000_000, "max_height": 1080},
	"720p":     {"max_bitrate": 4_000_000, "max_height": 720},
}

func init() {
	registerSchema(
		`CREATE TABLE IF NOT EXISTS downloads(
			id TEXT PRIMARY KEY,
			device_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			media_id INTEGER NOT NULL
		) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS downloads_device ON downloads(device_id)`,
	)
	handleRoute(http.MethodPost, "/downloads", DeviceOnly, createDownload)
	handleRoute(http.MethodGet, "/downloads/{downloadId}", DeviceOnly, getDownload)
	handleRoute(
		http.MethodDelete,
		"/downloads/{downloadId}",
		DeviceOnly,
		deleteDownload,
	)
	handleRoute(
		http.MethodPost,
		"/downloads/{downloadId}/complete",
		DeviceOnly,
		completeDownload,
	)
}

type downloadError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type downloadJob struct {
	ID        string         `json:"id"`
	MediaID   string         `json:"media_id"`
	State     string         `json:"state"`
	Progress  float64        `json:"progress"`
	Size      *int64         `json:"size"`
	Container *string        `json:"container"`
	URL       *string        `json:"url"`
	Error     *downloadError `json:"error,omitempty"`
}

type downloadRequest struct {
	MediaID      *string         `json:"media_id"`
	Quality      string          `json:"quality"`
	AudioIndexes []int           `json:"audio_indexes"`
	Capabilities json.RawMessage `json:"capabilities"`
}

func (d downloadRequest) valid() bool {
	return d.MediaID != nil && downloadCaps[d.Quality] != nil &&
		len(d.AudioIndexes) > 0 && !slices.ContainsFunc(
		d.AudioIndexes, func(i int) bool { return i < 0 },
	) && bytes.HasPrefix(bytes.TrimSpace(d.Capabilities), []byte("{"))
}

func invalidDownload() sdk.Response {
	return fail(http.StatusBadRequest, "invalid_request",
		"The body needs media_id, quality, audio_indexes and capabilities")
}

// hasAudio reports whether every index names an audio stream of the analysis.
func hasAudio(analysis json.RawMessage, indexes []int) bool {
	var a struct {
		Audio []struct {
			Index int `json:"index"`
		} `json:"audio"`
	}
	if json.Unmarshal(analysis, &a) != nil {
		return true
	}
	have := map[int]bool{}
	for _, s := range a.Audio {
		have[s.Index] = true
	}
	return !slices.ContainsFunc(indexes, func(i int) bool { return !have[i] })
}

// downloadJobID is the same for the same device, file, quality and audio set.
func downloadJobID(base string, deviceID int64, quality string, audio []int) string {
	sum := md5.Sum( //nolint:gosec // see the import
		fmt.Appendf(nil, "%s|%d|%s|%v", base, deviceID, quality, audio),
	)
	return hex.EncodeToString(sum[:])
}

func createDownload(c *call) sdk.Response {
	rememberFolders(c.R)
	var body downloadRequest
	if err := decodeBody(c, &body); err != nil || !body.valid() {
		return invalidDownload()
	}
	f, refused := lookupFile(c, *body.MediaID)
	if refused != nil {
		if refused.Status == http.StatusNotFound {
			return fail(http.StatusNotFound, "not_found", "No such media")
		}
		return *refused
	}
	guest, _ := c.R.GuestPath(f.Path)
	audio := slices.Compact(slices.Sorted(slices.Values(body.AudioIndexes)))

	analysis, err := analysisOf(f, guest)
	if err != nil {
		return engineUnavailable()
	}
	if analysis != nil && !hasAudio(analysis, audio) {
		return fail(http.StatusBadRequest, "invalid_request",
			"The file has no such audio track")
	}

	base, err := playJobID("download", f.Path, guest)
	if err != nil {
		return fail(http.StatusNotFound, "not_found", "No such media")
	}
	id := downloadJobID(base, c.Device.ID, body.Quality, audio)
	spec := map[string]any{
		"job_id": id, "type": "download", "priority": "interactive",
		"path": f.Path, "capabilities": body.Capabilities,
		"audio_indexes": audio,
	}
	maps.Copy(spec, downloadCaps[body.Quality])
	status, raw, err := engineFetch(http.MethodPost, "/v1/jobs", spec)
	if err != nil {
		return engineUnavailable()
	}
	if status/100 != 2 && !jobExists(raw) {
		return engineError(status, raw)
	}
	if err := exec(
		`INSERT INTO downloads(id, device_id, user_id, media_id)
		VALUES(?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		id, c.Device.ID, c.User.ID, f.ID,
	); err != nil {
		return internalError("saving a download", err)
	}
	job, refused := downloadView(id, f.ID)
	if refused != nil {
		return *refused
	}
	return jsonOK(http.StatusCreated, job)
}

func jobExists(raw []byte) bool {
	var e struct {
		Code string `json:"code"`
	}
	return json.Unmarshal(raw, &e) == nil && e.Code == "job_exists"
}

// downloadView reads the job from the engine. refused is set when the engine
// cannot be asked, which is not the job failing.
func downloadView(
	id string,
	mediaID int64,
) (job downloadJob, refused *sdk.Response) {
	job = downloadJob{
		ID: id, MediaID: fileIDString(mediaID), State: "preparing",
	}
	failed := func(message string) downloadJob {
		if message == "" {
			message = "The server couldn't prepare it."
		}
		job.State = "failed"
		job.Error = &downloadError{Code: "prepare_failed", Message: message}
		return job
	}
	status, raw, err := engineFetch(http.MethodGet, "/v1/jobs/"+id, nil)
	if err != nil || status == http.StatusUnauthorized {
		r := engineUnavailable()
		return job, &r
	}
	var js struct {
		State    string   `json:"state"`
		Progress *float64 `json:"progress"`
		Result   struct {
			Size      int64  `json:"size"`
			Container string `json:"container"`
		} `json:"result"`
		Error string `json:"error"`
	}
	if status != http.StatusOK || json.Unmarshal(raw, &js) != nil ||
		js.State == "failed" {
		return failed(js.Error), nil
	}
	if js.State != "done" {
		if js.Progress != nil {
			job.Progress = *js.Progress
		}
		return job, nil
	}
	signer, ok := newSigner()
	if !ok {
		r := engineUnavailable()
		return job, &r
	}
	url := signer.signed("dl", id, "dl/"+id, mediaTTL)
	job.State, job.Progress = "ready", 1
	job.Size, job.Container, job.URL = &js.Result.Size, &js.Result.Container, &url
	return job, nil
}

// ownDownload is the media a download of this device is for.
func ownDownload(c *call) (mediaID int64, ok bool, err error) {
	id := c.Params["downloadId"]
	if !sessionIDPattern.MatchString(id) {
		return 0, false, nil
	}
	res, err := query(
		`SELECT media_id FROM downloads WHERE id = ? AND device_id = ?`,
		id, c.Device.ID,
	)
	if err != nil || len(res.Rows) == 0 {
		return 0, false, err
	}
	return row(res.Rows[0]).int(0), true, nil
}

func downloadNotFound() sdk.Response {
	return fail(http.StatusNotFound, "not_found", "No such download")
}

func getDownload(c *call) sdk.Response {
	mediaID, ok, err := ownDownload(c)
	if err != nil {
		return internalError("reading a download", err)
	}
	if !ok {
		return downloadNotFound()
	}
	job, refused := downloadView(c.Params["downloadId"], mediaID)
	if refused != nil {
		return *refused
	}
	return jsonOK(http.StatusOK, job)
}

func dropDownload(id string) error {
	if err := exec(`DELETE FROM downloads WHERE id = ?`, id); err != nil {
		return err
	}
	if _, _, err := engineFetch(http.MethodDelete, "/v1/jobs/"+id, nil); err != nil {
		logFailure("releasing a download job", err)
	}
	return nil
}

func deleteDownload(c *call) sdk.Response {
	_, ok, err := ownDownload(c)
	if err != nil {
		return internalError("reading a download", err)
	}
	if ok {
		if err := dropDownload(c.Params["downloadId"]); err != nil {
			return internalError("deleting a download", err)
		}
	}
	return sdk.Response{Status: http.StatusNoContent}
}

func completeDownload(c *call) sdk.Response {
	mediaID, ok, err := ownDownload(c)
	if err != nil {
		return internalError("reading a download", err)
	}
	if !ok {
		return downloadNotFound()
	}
	id := c.Params["downloadId"]
	job, refused := downloadView(id, mediaID)
	if refused != nil {
		return *refused
	}
	if job.State != "ready" {
		return fail(http.StatusConflict, "not_ready", "The download is not ready")
	}
	if err := insertDownloadEvent(c, mediaID); err != nil {
		return internalError("recording a download", err)
	}
	if err := dropDownload(id); err != nil {
		return internalError("deleting a download", err)
	}
	return sdk.Response{Status: http.StatusNoContent}
}

func insertDownloadEvent(c *call, mediaID int64) error {
	body := map[string]any{
		"device": map[string]any{
			"name": c.Device.Name, "platform": c.Device.Platform,
		},
	}
	res, err := query(
		`SELECT movie_id, episode_id FROM streamline_video_files WHERE id = ?`,
		mediaID,
	)
	if err != nil {
		return err
	}
	if len(res.Rows) == 1 {
		r := row(res.Rows[0])
		if title, place, ok := titleRef(r.int(0), r.int(1)); ok {
			body["title"] = title
			maps.Copy(body, place)
		}
	}
	return insertEvent("download", c.Device.ID, body)
}
