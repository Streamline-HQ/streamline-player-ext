package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/scan"
)

func init() {
	registerSchema(
		`CREATE TABLE IF NOT EXISTS analysis(
			path TEXT PRIMARY KEY,
			version INTEGER NOT NULL,
			json TEXT NOT NULL
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS markers(
			media_id INTEGER PRIMARY KEY,
			season_key TEXT NOT NULL,
			intro_start REAL,
			intro_end REAL,
			credits_start REAL,
			credits_end REAL
		)`,
		`CREATE INDEX IF NOT EXISTS markers_season ON markers(season_key)`,
		`CREATE TABLE IF NOT EXISTS intro_checked(
			season_key TEXT PRIMARY KEY,
			job_id TEXT NOT NULL
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS jobs(
			job_id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			path TEXT NOT NULL,
			state TEXT NOT NULL
		)`,
		// When each job finished, for the pace a scan's time left goes by.
		`CREATE TABLE IF NOT EXISTS job_ends(
			job_id TEXT PRIMARY KEY,
			at INTEGER NOT NULL
		) WITHOUT ROWID`,
	)
	handleRoute(http.MethodPost, engineJobsPath+"{id}", Public, postEngineJob)
	handleRoute(http.MethodPost, engineContinuePath, Public, postEngineContinue)
	analysisSource = analysisFor
	markersSource = markersFor
}

const (
	engineJobsPath        = "/engine/jobs/"
	engineContinuePath    = "/engine/continue"
	engineSignatureHeader = "X-Engine-Signature"
	// callbackBaseKey keeps the address of the last admin request, for the
	// calls that carry none.
	callbackBaseKey  = "engine.callback_base"
	scanContinuation = "scan"
)

// signedByEngine says whether the request's body carries the engine's
// signature. The engine's routes take no user credential: this is theirs.
func signedByEngine(r sdk.Request) bool {
	secret, ok := engineSecret()
	if !ok {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(r.Body)
	given, err := hex.DecodeString(
		http.Header(r.Headers).Get(engineSignatureHeader),
	)
	return err == nil && hmac.Equal(given, mac.Sum(nil))
}

// callbackURL is this extension's address for the engine, ending in path, or
// "" when no admin request has given one yet.
func callbackURL(path string) string {
	base := callbackBase
	if base == "" {
		base, _ = setting[string](callbackBaseKey)
	}
	if base == "" {
		return ""
	}
	return base + path
}

// continueLater asks the engine to keep calling until the comparison is done:
// a call here ends after its budget, and nothing else would start the next.
func continueLater() {
	url := callbackURL(engineContinuePath)
	if url == "" {
		return
	}
	if _, err := engineDo(
		http.MethodPut, "/v1/continuations/"+scanContinuation,
		map[string]string{"url": url}, nil,
	); err != nil && !errors.Is(err, errEngineNotConfigured) {
		logFailure("asking the engine to carry the comparison on", err)
	}
}

// postEngineContinue is what the engine calls while a comparison is
// unfinished. 202 asks for the next call, 204 ends them.
func postEngineContinue(c *call) sdk.Response {
	if !signedByEngine(c.R) {
		return fail(http.StatusUnauthorized, "unauthorized", "Unauthorized")
	}
	if len(c.R.Folders) > 0 {
		callFolders = c.R.Folders
	}
	done, err := bringUpToDate(scanScope{}, compareBudget)
	if err != nil {
		return libraryFailed(err)
	}
	if !done {
		return sdk.Response{Status: http.StatusAccepted}
	}
	return sdk.Response{Status: http.StatusNoContent}
}

// postEngineJob is where the engine posts a finished job's result.
func postEngineJob(c *call) sdk.Response {
	if !signedByEngine(c.R) {
		return fail(http.StatusUnauthorized, "unauthorized", "Unauthorized")
	}
	var body struct {
		ID     string          `json:"job_id"`
		State  string          `json:"state"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(c.R.Body, &body) != nil || body.ID != c.Params["id"] {
		return fail(http.StatusBadRequest, "invalid_request", "Invalid request")
	}
	res, err := query(
		`SELECT kind, path FROM jobs WHERE job_id = ? AND state = ?`,
		body.ID, jobQueued)
	if err != nil {
		return internalError("reading a job", err)
	}
	if len(res.Rows) == 0 {
		return fail(http.StatusNotFound, "not_found", "No such job")
	}
	rememberFolders(c.R)
	settleJob(body.ID, row(res.Rows[0]).str(0), row(res.Rows[0]).str(1),
		body.State, body.Result)
	return sdk.Response{Status: http.StatusNoContent}
}

const (
	jobQueued = "queued"
	jobDone   = "done"

	// intro results arrive with the paths of one season, so a job carries the
	// season's key where the others carry a file.
	seasonKeyPrefix = "season:"
)

// callFolders is what the last call that had folders could see. The engine
// is told host paths, and the guest stats them through this mapping.
var callFolders []sdk.Folder

// callbackBase is where the engine posts a finished job's result: this
// extension as the admin who started the comparison reached it. It stays
// empty for a call that came from the host, which has no address.
var callbackBase string

func rememberFolders(r sdk.Request) {
	if len(r.Folders) > 0 {
		callFolders = r.Folders
	}
	if r.Origin != "" {
		callbackBase = r.Origin + r.BasePath
	}
}

type fileStat struct{ size, mtimeNs int64 }

func statHost(hostPath string) (fileStat, error) {
	guest, ok := sdk.Request{Folders: callFolders}.GuestPath(hostPath)
	if !ok {
		return fileStat{}, errors.New("the file is outside the granted folders")
	}
	info, err := os.Stat(guest)
	if err != nil {
		return fileStat{}, err
	}
	return fileStat{info.Size(), info.ModTime().UnixNano()}, nil
}

// jobIDFor is the engine job id of a job on a file. A file that cannot be
// stat'ed here (no call has handed over the folders yet) falls back to the
// size and mtime the comparison recorded.
func jobIDFor(kind, path string) (string, error) {
	st, err := statHost(path)
	if err != nil {
		res, qerr := query(
			`SELECT size, mtime_ns FROM seen
			WHERE path = ? AND gone_at IS NULL ORDER BY video_file_id DESC LIMIT 1`,
			path)
		if qerr != nil || len(res.Rows) == 0 {
			return "", err
		}
		r := row(res.Rows[0])
		st = fileStat{r.int(0), parseNs(r.str(1))}
	}
	return scan.JobID(kind, path, st.size, st.mtimeNs), nil
}

// analysisFor is the stored result of the analyze job for a file. A job that
// finished since it was last looked at is collected first.
func analysisFor(path string) (json.RawMessage, bool) {
	read := func() (json.RawMessage, bool) {
		res, err := query(`SELECT json FROM analysis WHERE path = ?`, path)
		if err != nil || len(res.Rows) == 0 {
			return nil, false
		}
		return json.RawMessage(row(res.Rows[0]).str(0)), true
	}
	if a, ok := read(); ok {
		return a, true
	}
	res, err := query(
		`SELECT job_id FROM jobs WHERE kind = 'analyze' AND path = ? AND state = ?`,
		path, jobQueued)
	if err != nil {
		return nil, false
	}
	for _, r := range res.Rows {
		collectOne(row(r).str(0), "analyze", path)
	}
	return read()
}

// markersFor is the intro and credits span stored for a file.
func markersFor(mediaID int64) (intro, credits *[2]float64) {
	read := func() (*sdk.SQLResult, bool) {
		res, err := query(
			`SELECT intro_start, intro_end, credits_start, credits_end
			FROM markers WHERE media_id = ?`, mediaID)
		if err != nil || len(res.Rows) == 0 {
			return nil, false
		}
		return &res, true
	}
	res, ok := read()
	if !ok {
		collectJobs(collectBatch)
		if res, ok = read(); !ok {
			return nil, nil
		}
	}
	r := row(res.Rows[0])
	span := func(a, b int) *[2]float64 {
		if r[a] == nil || r[b] == nil {
			return nil
		}
		return &[2]float64{libFloat(r[a]), libFloat(r[b])}
	}
	return span(0, 1), span(2, 3)
}

const collectBatch = 8

type engineJobSpec struct {
	ID, Kind, Path string
	Paths          []string
}

func (j engineJobSpec) body() map[string]any {
	m := map[string]any{
		"job_id": j.ID, "type": j.Kind, "priority": "background",
	}
	if url := callbackURL(engineJobsPath + j.ID); url != "" {
		m["callback"] = url
	}
	if j.Kind == "intro" {
		m["paths"] = j.Paths
	} else {
		m["path"] = j.Path
	}
	return m
}

// submitJobs sends the jobs to the engine in one list and returns those it
// has now, either newly queued or already known. failed counts the ones it
// refused; an engine that answered nothing is reported as reachable false.
func submitJobs(
	specs []engineJobSpec,
) (accepted []engineJobSpec, failed int, reachable bool) {
	if len(specs) == 0 {
		return nil, 0, true
	}
	body := make([]map[string]any, len(specs))
	for i, s := range specs {
		body[i] = s.body()
	}
	var out struct {
		Results []struct {
			Error *struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"results"`
	}
	status, err := engineDo(http.MethodPost, "/v1/jobs", body, &out)
	if err != nil {
		return nil, 0, false
	}
	if status != http.StatusAccepted || len(out.Results) != len(specs) {
		return nil, len(specs), true
	}
	for i, r := range out.Results {
		if r.Error == nil || r.Error.Code == "job_exists" {
			accepted = append(accepted, specs[i])
		} else {
			failed++
		}
	}
	return accepted, failed, true
}

func recordJobs(specs []engineJobSpec) error {
	for _, s := range specs {
		if err := exec(
			`INSERT INTO jobs(job_id, kind, path, state) VALUES(?, ?, ?, ?)
			ON CONFLICT(job_id) DO UPDATE SET state = excluded.state`,
			s.ID, s.Kind, s.Path, jobQueued,
		); err != nil {
			return err
		}
	}
	return nil
}

// engineAnalysisVersion is the analysis version the engine produces now.
func engineAnalysisVersion() (int, bool) {
	h, ok := readEngineState()
	return h.AnalysisVersion, ok
}

type engineState struct {
	AnalysisVersion int `json:"analysis_version"`
	// JobsOwed is how many accepted jobs the engine has yet to finish or to
	// post the result of. Nil while it is still reading its queue back after
	// a restart, and from an engine that does not report it.
	JobsOwed *int64 `json:"jobs_owed"`
	// JobsRunning is how far each job the engine is on says it is, 0 to 1.
	JobsRunning map[string]float64 `json:"jobs_running"`
}

func readEngineState() (engineState, bool) {
	var h engineState
	status, err := engineDo(http.MethodGet, "/v1/health", nil, &h)
	return h, err == nil && status == http.StatusOK
}

// collectJobs asks the engine about up to limit waiting jobs and stores what
// the finished ones produced. The engine posts each result itself; this is
// what catches one whose post never arrived. The jobs are drawn at random: a
// library scan leaves tens of thousands waiting, too many to read into a
// guest on every call, and the oldest ones are the last the engine gets to.
func collectJobs(limit int) {
	res, err := query(
		`SELECT job_id, kind, path FROM jobs WHERE state = ?
		ORDER BY random() LIMIT ?`,
		jobQueued, limit)
	if err != nil {
		return
	}
	for _, r := range res.Rows {
		if !collectOne(row(r).str(0), row(r).str(1), row(r).str(2)) {
			return
		}
	}
}

// collectOne settles one job from the engine's answer about it, and says
// whether the engine answered. A job the engine no longer knows is forgotten,
// so the next comparison submits it again.
func collectOne(id, kind, path string) (answered bool) {
	var st struct {
		State  string          `json:"state"`
		Result json.RawMessage `json:"result"`
	}
	status, err := engineDo(http.MethodGet, "/v1/jobs/"+id, nil, &st)
	switch {
	case err != nil:
		return false
	case status == http.StatusNotFound:
		_ = exec(`DELETE FROM jobs WHERE job_id = ?`, id)
		return true
	case status != http.StatusOK || st.State == jobQueued || st.State == "running":
		return true
	}
	settleJob(id, kind, path, st.State, st.Result)
	return true
}

// settleJob stores what a finished job produced and releases it in the
// engine. A failed job is forgotten: the next comparison submits it again.
func settleJob(id, kind, path, state string, result json.RawMessage) {
	if state != jobDone {
		_ = exec(`DELETE FROM jobs WHERE job_id = ?`, id)
		return
	}
	if kind != "analyze" && kind != "intro" {
		markJobDone(id)
		return
	}
	if kind == "analyze" && supersededAnalysis(id, path) {
		_, _ = engineDo(http.MethodDelete, "/v1/jobs/"+id, nil, nil)
		_ = exec(`DELETE FROM jobs WHERE job_id = ?`, id)
		return
	}
	var stored error
	if kind == "analyze" {
		stored = storeAnalysis(path, result)
	} else {
		stored = storeMarkers(id, path, result)
	}
	if stored != nil {
		sdk.Log("warn", "storing a job result failed",
			"job", id, "kind", kind, "error", stored.Error())
		return
	}
	_, _ = engineDo(http.MethodDelete, "/v1/jobs/"+id, nil, nil)
	markJobDone(id)
}

func markJobDone(id string) {
	_ = exec(`UPDATE jobs SET state = ? WHERE job_id = ?`, jobDone, id)
	_ = exec(
		`INSERT INTO job_ends(job_id, at) VALUES(?, ?)
		ON CONFLICT(job_id) DO NOTHING`, id, now().Unix())
}

// supersededAnalysis says whether the file changed since the analyze job id
// was made: its result describes bytes that are no longer there, and the
// newer file's own job is the one to keep.
func supersededAnalysis(id, path string) bool {
	current, err := jobIDFor("analyze", path)
	return err == nil && current != id
}

func storeAnalysis(path string, result json.RawMessage) error {
	var v struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(result, &v); err != nil {
		return err
	}
	return exec(
		`INSERT INTO analysis(path, version, json) VALUES(?, ?, ?)
		ON CONFLICT(path) DO UPDATE
		SET version = excluded.version, json = excluded.json`,
		path, v.Version, string(result))
}

// storeMarkers keeps what the intro job id found for a season, and records
// that the season's files as the id names them were checked, even when no
// episode had a span: a season without an intro leaves no markers behind.
func storeMarkers(id, seasonKey string, result json.RawMessage) error {
	var found []struct {
		Path    string      `json:"path"`
		Intro   *[2]float64 `json:"intro"`
		Credits *[2]float64 `json:"credits"`
	}
	if err := json.Unmarshal(result, &found); err != nil {
		return err
	}
	if err := exec(
		`DELETE FROM markers WHERE season_key = ?`,
		seasonKey,
	); err != nil {
		return err
	}
	for _, f := range found {
		res, err := query(
			`SELECT video_file_id FROM seen
			WHERE path = ? AND gone_at IS NULL ORDER BY video_file_id DESC LIMIT 1`,
			f.Path)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 || (f.Intro == nil && f.Credits == nil) {
			continue
		}
		args := []any{row(res.Rows[0]).int(0), seasonKey, nil, nil, nil, nil}
		if f.Intro != nil {
			args[2], args[3] = f.Intro[0], f.Intro[1]
		}
		if f.Credits != nil {
			args[4], args[5] = f.Credits[0], f.Credits[1]
		}
		if err := exec(
			`INSERT OR REPLACE INTO markers(media_id, season_key, intro_start,
				intro_end, credits_start, credits_end) VALUES(?, ?, ?, ?, ?, ?)`,
			args...); err != nil {
			return err
		}
	}
	return exec(
		`INSERT INTO intro_checked(season_key, job_id) VALUES(?, ?)
		ON CONFLICT(season_key) DO UPDATE SET job_id = excluded.job_id`,
		seasonKey, id)
}

func seasonKey(id int64) string { return seasonKeyPrefix + fileIDString(id) }

func joinPaths(paths []string) string { return strings.Join(paths, "\n") }
