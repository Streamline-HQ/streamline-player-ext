package main

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/scan"
)

func init() {
	registerSchema(
		`CREATE TABLE IF NOT EXISTS seen(
			video_file_id INTEGER PRIMARY KEY,
			path TEXT NOT NULL,
			size INTEGER NOT NULL,
			mtime_ns TEXT NOT NULL,
			movie_id INTEGER,
			episode_id INTEGER,
			seen_at INTEGER NOT NULL,
			gone_at INTEGER
		)`,
		`CREATE INDEX IF NOT EXISTS seen_movie ON seen(movie_id)`,
		`CREATE INDEX IF NOT EXISTS seen_episode ON seen(episode_id)`,
		`CREATE INDEX IF NOT EXISTS seen_path ON seen(path)`,
		`CREATE TABLE IF NOT EXISTS scan_state(
			id INTEGER PRIMARY KEY CHECK (id = 1),
			cursor INTEGER NOT NULL,
			scope TEXT NOT NULL,
			started_at INTEGER NOT NULL,
			finished_at INTEGER,
			added INTEGER NOT NULL DEFAULT 0,
			replaced INTEGER NOT NULL DEFAULT 0,
			errors INTEGER NOT NULL DEFAULT 0,
			last_at INTEGER,
			last_added INTEGER NOT NULL DEFAULT 0,
			last_replaced INTEGER NOT NULL DEFAULT 0,
			last_errors INTEGER NOT NULL DEFAULT 0
		)`,
	)
	handleRoute(http.MethodGet, "/admin/library", Admin, adminLibrary)
	handleRoute(http.MethodPost, "/admin/library/scan", Admin, adminScan)
}

const (
	pageSize = 200
	// A request has 30 s; the comparison stops starting pages after this.
	compareBudget = 20 * time.Second
	// maxID bounds an id window that runs to the end of the view. It stays
	// below 2^53 so it survives the host's JSON numbers.
	maxID = 1 << 52
)

// libraryEvent is what a comparison tells the activity feed: a file that
// arrived, or one that replaced Previous.
type libraryEvent struct {
	Replaced bool
	File     scan.File
	Previous *scan.Seen
}

var addEvent = func(libraryEvent) {}

type scanScope struct {
	Full     []string `json:"full,omitempty"`
	Movies   []int64  `json:"movies,omitempty"`
	Episodes []int64  `json:"episodes,omitempty"`
}

func (s scanScope) empty() bool {
	return len(s.Full) == 0 && len(s.Movies) == 0 && len(s.Episodes) == 0
}

func (s scanScope) merge(o scanScope) scanScope {
	union := func(a, b []string) []string {
		return slices.Compact(
			slices.Sorted(slices.Values(append(slices.Clone(a), b...))),
		)
	}
	unionIDs := func(a, b []int64) []int64 {
		return slices.Compact(
			slices.Sorted(slices.Values(append(slices.Clone(a), b...))),
		)
	}
	return scanScope{
		Full:     union(s.Full, o.Full),
		Movies:   unionIDs(s.Movies, o.Movies),
		Episodes: unionIDs(s.Episodes, o.Episodes),
	}.normalized()
}

// normalized drops the ids a full folder already takes in.
func (s scanScope) normalized() scanScope {
	if slices.Contains(s.Full, "movies") {
		s.Movies = nil
	}
	if slices.Contains(s.Full, "series") {
		s.Episodes = nil
	}
	return s
}

func (s scanScope) equal(o scanScope) bool {
	return slices.Equal(s.Full, o.Full) && slices.Equal(s.Movies, o.Movies) &&
		slices.Equal(s.Episodes, o.Episodes)
}

func (s scanScope) covers(folder string) bool {
	if slices.Contains(s.Full, folder) {
		return true
	}
	if folder == "movies" {
		return len(s.Movies) > 0
	}
	return len(s.Episodes) > 0
}

// only is the part of the scope that lies in folder.
func (s scanScope) only(folder string) scanScope {
	var o scanScope
	if slices.Contains(s.Full, folder) {
		o.Full = []string{folder}
	}
	if folder == "movies" {
		o.Movies = s.Movies
	} else {
		o.Episodes = s.Episodes
	}
	return o
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// where is a condition on the owner columns of the view or of seen, which
// are named alike.
func (s scanScope) where() (string, []any) {
	var parts []string
	var args []any
	if slices.Contains(s.Full, "movies") {
		parts = append(parts, "movie_id IS NOT NULL")
	}
	if slices.Contains(s.Full, "series") {
		parts = append(parts, "episode_id IS NOT NULL")
	}
	if len(s.Movies) > 0 {
		parts = append(parts, "movie_id IN ("+placeholders(len(s.Movies))+")")
		for _, id := range s.Movies {
			args = append(args, id)
		}
	}
	if len(s.Episodes) > 0 {
		parts = append(parts, "episode_id IN ("+placeholders(len(s.Episodes))+")")
		for _, id := range s.Episodes {
			args = append(args, id)
		}
	}
	if len(parts) == 0 {
		return "0", nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

type scanState struct {
	exists                  bool
	cursor                  int64
	scope                   scanScope
	rawScope                string
	startedAt, finishedAt   int64
	added, replaced, errors int64
}

func (s scanState) finished() bool { return !s.exists || s.finishedAt != 0 }

func loadScanState() (scanState, error) {
	res, err := query(
		`SELECT cursor, scope, started_at, ifnull(finished_at, 0), added,
			replaced, errors FROM scan_state WHERE id = 1`)
	if err != nil || len(res.Rows) == 0 {
		return scanState{}, err
	}
	r := row(res.Rows[0])
	st := scanState{
		exists: true, cursor: r.int(0), rawScope: r.str(1), startedAt: r.int(2),
		finishedAt: r.int(3), added: r.int(4), replaced: r.int(5),
		errors: r.int(6),
	}
	_ = json.Unmarshal([]byte(r.str(1)), &st.scope)
	return st, nil
}

func saveScanState(st scanState) error {
	scope, _ := json.Marshal(st.scope)
	var finished any
	if st.finishedAt != 0 {
		finished = st.finishedAt
	}
	if err := exec(
		`INSERT INTO scan_state(id, cursor, scope, started_at, finished_at,
			added, replaced, errors) VALUES(1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET cursor = excluded.cursor,
			scope = excluded.scope, started_at = excluded.started_at,
			finished_at = excluded.finished_at, added = excluded.added,
			replaced = excluded.replaced, errors = excluded.errors`,
		st.cursor, string(scope), st.startedAt, finished,
		st.added, st.replaced, st.errors,
	); err != nil {
		return err
	}
	if st.finishedAt == 0 || len(st.scope.Full) == 0 {
		return nil
	}
	return exec(
		`UPDATE scan_state SET last_at = ?, last_added = ?, last_replaced = ?,
			last_errors = ? WHERE id = 1`,
		st.finishedAt, st.added, st.replaced, st.errors)
}

// scanLeaseKey names the meta row that says a request is comparing.
const (
	scanLeaseKey    = "scan_lease"
	scanLeaseMargin = 10 * time.Second
)

// claimScan takes the right to run comparison pages for as long as budget
// allows. Requests run side by side, and two of them on one page would
// classify the same rows twice and each hold a store connection for the
// whole call. A claim that is never released, because the request was cut
// short, runs out by itself.
func claimScan(budget time.Duration) (bool, error) {
	res, err := run(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
		WHERE CAST(meta.value AS INTEGER) < ?`,
		scanLeaseKey, now().Add(budget+scanLeaseMargin).Unix(), now().Unix(),
	)
	return err == nil && res.RowsAffected > 0, err
}

func releaseScan() {
	if err := exec(
		`UPDATE meta SET value = '0' WHERE key = ?`, scanLeaseKey,
	); err != nil {
		logFailure("releasing the comparison", err)
	}
}

// registerScope makes scope part of the comparison: it starts one when none
// is running, and otherwise adds what the running one does not cover yet,
// which sends it back to the first row. A folder already being compared is
// left alone.
func registerScope(scope scanScope) error {
	if scope.empty() {
		return nil
	}
	for range 3 {
		st, err := loadScanState()
		if err != nil {
			return err
		}
		if st.finished() {
			// The engine may still be working through an earlier scan's jobs
			// when a change to one title starts a comparison. The time left
			// is measured from that scan's start, so it stays.
			started := now().Unix()
			if st.exists && jobsWaiting() > 0 {
				started = st.startedAt
			}
			return saveScanState(scanState{
				exists: true, scope: scope.normalized(), startedAt: started,
			})
		}
		merged := st.scope.merge(scope)
		if merged.equal(st.scope) {
			return nil
		}
		st.scope, st.cursor = merged, 0
		if ok, err := advanceScan(st, st.rawScope); err != nil || ok {
			return err
		}
	}
	return nil
}

// advanceScan writes an unfinished comparison's progress, unless its scope
// changed or it finished since prevScope was read.
func advanceScan(st scanState, prevScope string) (bool, error) {
	scope, _ := json.Marshal(st.scope)
	var finished any
	if st.finishedAt != 0 {
		finished = st.finishedAt
	}
	res, err := run(
		`UPDATE scan_state SET cursor = ?, scope = ?, finished_at = ?, added = ?,
			replaced = ?, errors = ? WHERE id = 1 AND scope = ?
			AND finished_at IS NULL`,
		st.cursor, string(scope), finished, st.added, st.replaced, st.errors,
		prevScope,
	)
	if err != nil || res.RowsAffected == 0 {
		return false, err
	}
	if st.finishedAt == 0 || len(st.scope.Full) == 0 {
		return true, nil
	}
	return true, exec(
		`UPDATE scan_state SET last_at = ?, last_added = ?, last_replaced = ?,
			last_errors = ? WHERE id = 1`,
		st.finishedAt, st.added, st.replaced, st.errors)
}

// compare brings the record of seen files up to date with the view, for the
// files scope names, and queues the engine jobs the changes call for. It
// stops after budget and answers done false; the cursor is kept, so the
// next call goes on where this one stopped. A scope that is not empty joins
// the comparison already running, or starts a new one. Only one request at a
// time runs pages; the others leave that to it.
func compare(scope scanScope, budget time.Duration) (done bool, err error) {
	if err := registerScope(scope); err != nil {
		return false, err
	}
	st, err := loadScanState()
	if err != nil || st.finished() {
		return err == nil, err
	}
	claimed, err := claimScan(budget)
	if err != nil || !claimed {
		return false, err
	}
	defer releaseScan()
	q := newJobQueue()
	deadline := now().Add(budget)
	for {
		if st, err = loadScanState(); err != nil {
			return false, err
		}
		if st.finished() {
			return true, nil
		}
		last, err := comparePage(&st, q)
		if err != nil {
			return false, err
		}
		if last {
			st.finishedAt = now().Unix()
		}
		advanced, err := advanceScan(st, st.rawScope)
		if err != nil {
			return false, err
		}
		if advanced && last {
			return true, nil
		}
		if !now().Before(deadline) {
			return false, nil
		}
	}
}

// jobQueue is what one comparison knows about the engine: its analysis
// version, whether it answers, and the seasons whose intro job it has looked
// at.
type jobQueue struct {
	version   int
	reachable bool
	seasons   map[int64]bool
}

func newJobQueue() *jobQueue {
	q := &jobQueue{seasons: map[int64]bool{}}
	q.version, q.reachable = engineAnalysisVersion()
	return q
}

// Nanosecond times are past 2^53, so they cross the host as text.
func parseNs(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func nullable(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func fileOf(r row) scan.File {
	return scan.File{
		ID: r.int(0), MovieID: r.int(1), EpisodeID: r.int(2), Path: r.str(3),
		Size: r.int(4), Missing: r.int(5) != 0,
	}
}

func seenRow(r row) scan.Seen {
	return scan.Seen{
		ID: r.int(0), MovieID: r.int(1), EpisodeID: r.int(2), Path: r.str(3),
		Size: r.int(4), MtimeNs: parseNs(r.str(5)), GoneAt: r.int(6),
	}
}

const seenColumns = `video_file_id, ifnull(movie_id, 0), ifnull(episode_id, 0),
	path, size, mtime_ns, ifnull(gone_at, 0)`

// comparePage handles the next page of the view and says whether it was the
// last. The record and the cursor are written together, after the engine has
// been told, so a request cut short repeats a page rather than skipping one.
func comparePage(st *scanState, q *jobQueue) (last bool, err error) {
	cond, scopeArgs := st.scope.where()
	res, err := query(
		`SELECT id, ifnull(movie_id, 0), ifnull(episode_id, 0), path, size,
			missing FROM streamline_video_files
		WHERE id > ? AND `+cond+` ORDER BY id LIMIT ?`,
		append(append([]any{st.cursor}, scopeArgs...), pageSize)...)
	if err != nil {
		return false, err
	}
	page := make([]scan.File, 0, len(res.Rows))
	for _, r := range res.Rows {
		page = append(page, fileOf(row(r)))
	}
	last = len(page) < pageSize
	hi := int64(maxID)
	if !last {
		hi = page[len(page)-1].ID
	}

	seen, err := seenWindow(st, cond, scopeArgs, hi, page)
	if err != nil {
		return false, err
	}
	stats := map[int64]fileStat{}
	unreadable := map[int64]bool{}
	var usable []scan.File
	for _, f := range page {
		if f.Missing {
			usable = append(usable, f)
			continue
		}
		stat, err := statHost(f.Path)
		if err != nil {
			st.errors++
			unreadable[f.ID] = true
			continue
		}
		f.MtimeNs = stat.mtimeNs
		stats[f.ID] = stat
		usable = append(usable, f)
	}
	seen = slices.DeleteFunc(
		seen,
		func(s scan.Seen) bool { return unreadable[s.ID] },
	)
	changes := scan.Classify(seen, usable)

	var specs []engineJobSpec
	changed := map[int64]bool{}
	var seasonFiles, keptEpisodes []scan.File
	for _, c := range changes {
		if c.Kind == scan.Gone {
			continue
		}
		changed[c.File.ID] = true
		specs = append(specs, fileJobs(c.File.Path, stats[c.File.ID])...)
		if c.File.EpisodeID != 0 {
			seasonFiles = append(seasonFiles, c.File)
		}
	}
	liveSeen := map[int64]scan.Seen{}
	for _, s := range seen {
		if s.GoneAt == 0 {
			liveSeen[s.ID] = s
		}
	}
	for _, f := range usable {
		if _, known := liveSeen[f.ID]; !known || changed[f.ID] || f.Missing {
			continue
		}
		if q.wants(f.Path) {
			specs = append(specs, fileJobs(f.Path, stats[f.ID])...)
		}
		if f.EpisodeID != 0 {
			keptEpisodes = append(keptEpisodes, f)
		}
	}
	changedSeasons, err := episodeSeasons(seasonFiles)
	if err != nil {
		return false, err
	}
	intro, err := seasonJobs(changedSeasons)
	if err != nil {
		return false, err
	}
	for _, s := range changedSeasons {
		q.seasons[s] = true
	}
	keptSeasons, err := episodeSeasons(keptEpisodes)
	if err != nil {
		return false, err
	}
	lostIntro, err := q.introJobs(keptSeasons)
	if err != nil {
		return false, err
	}
	specs = append(append(specs, intro...), lostIntro...)
	accepted, failed := q.submit(specs)
	st.errors += int64(failed)

	if err := apply(st, changes, accepted); err != nil {
		return false, err
	}
	st.cursor = hi
	if !last {
		st.cursor = page[len(page)-1].ID
	}
	return last, nil
}

// seenWindow is the record's rows for the page's id window, plus the rows of
// the page's owners that went away earlier, which a replacement names.
func seenWindow(
	st *scanState, cond string, scopeArgs []any, hi int64, page []scan.File,
) ([]scan.Seen, error) {
	res, err := query(
		`SELECT `+seenColumns+` FROM seen
		WHERE video_file_id > ? AND video_file_id <= ? AND `+cond,
		append([]any{st.cursor, hi}, scopeArgs...)...)
	if err != nil {
		return nil, err
	}
	var seen []scan.Seen
	have := map[int64]bool{}
	for _, r := range res.Rows {
		s := seenRow(row(r))
		have[s.ID] = true
		seen = append(seen, s)
	}
	var movies, episodes []any
	for _, f := range page {
		if have[f.ID] {
			continue
		}
		if f.MovieID != 0 {
			movies = append(movies, f.MovieID)
		}
		if f.EpisodeID != 0 {
			episodes = append(episodes, f.EpisodeID)
		}
	}
	for _, o := range []struct {
		col string
		ids []any
	}{{"movie_id", movies}, {"episode_id", episodes}} {
		if len(o.ids) == 0 {
			continue
		}
		res, err := query(
			`SELECT `+seenColumns+` FROM seen WHERE gone_at IS NOT NULL
			AND `+o.col+` IN (`+placeholders(len(o.ids))+`)`, o.ids...)
		if err != nil {
			return nil, err
		}
		for _, r := range res.Rows {
			if s := seenRow(row(r)); !have[s.ID] {
				have[s.ID] = true
				seen = append(seen, s)
			}
		}
	}
	return seen, nil
}

// fileJobs are the jobs one file gets: its analysis, subtitle tracks and
// trickplay sheets.
func fileJobs(path string, st fileStat) []engineJobSpec {
	kinds := []string{"analyze", "subtitles", "trickplay"}
	out := make([]engineJobSpec, len(kinds))
	for i, k := range kinds {
		out[i] = engineJobSpec{
			ID: scan.JobID(k, path, st.size, st.mtimeNs), Kind: k, Path: path,
		}
	}
	return out
}

// wants says whether a file the record already holds needs its jobs again:
// it has no analysis of the engine's current version. The jobs are submitted
// whether or not the engine already has them: it answers that it does, which
// costs less than asking it for everything it holds, and brings back the ones
// an engine replaced without its jobs_dir forgot.
func (q *jobQueue) wants(path string) bool {
	if !q.reachable {
		return false
	}
	res, err := query(`SELECT version FROM analysis WHERE path = ?`, path)
	if err != nil {
		return false
	}
	return len(res.Rows) == 0 || row(res.Rows[0]).int(0) < int64(q.version)
}

func (q *jobQueue) submit(
	specs []engineJobSpec,
) (accepted []engineJobSpec, failed int) {
	if !q.reachable || len(specs) == 0 {
		return nil, 0
	}
	accepted, failed, q.reachable = submitJobs(specs)
	return accepted, failed
}

// introJobs is the intro job of each season, not looked at yet in this
// comparison, that has no markers stored and was not checked with its files
// as they are now.
func (q *jobQueue) introJobs(seasons []int64) ([]engineJobSpec, error) {
	if !q.reachable {
		return nil, nil
	}
	var bare []int64
	for _, s := range seasons {
		if q.seasons[s] {
			continue
		}
		q.seasons[s] = true
		res, err := query(
			`SELECT 1 FROM markers WHERE season_key = ? LIMIT 1`, seasonKey(s))
		if err != nil {
			return nil, err
		}
		if len(res.Rows) == 0 {
			bare = append(bare, s)
		}
	}
	specs, err := seasonJobs(bare)
	if err != nil {
		return nil, err
	}
	out := specs[:0]
	for _, j := range specs {
		res, err := query(
			`SELECT 1 FROM intro_checked WHERE season_key = ? AND job_id = ?`,
			j.Path, j.ID)
		if err != nil {
			return nil, err
		}
		if len(res.Rows) == 0 {
			out = append(out, j)
		}
	}
	return out, nil
}

func episodeSeasons(files []scan.File) ([]int64, error) {
	episodes := make([]any, 0, len(files))
	for _, f := range files {
		episodes = append(episodes, f.EpisodeID)
	}
	if len(episodes) == 0 {
		return nil, nil
	}
	res, err := query(
		`SELECT DISTINCT season_id FROM streamline_episodes
		WHERE id IN (`+placeholders(len(episodes))+`)`, episodes...)
	if err != nil {
		return nil, err
	}
	seasons := make([]int64, 0, len(res.Rows))
	for _, r := range res.Rows {
		seasons = append(seasons, row(r).int(0))
	}
	return seasons, nil
}

// seasonJobs is one intro job for each season that has at least two files to
// compare.
func seasonJobs(seasons []int64) ([]engineJobSpec, error) {
	var out []engineJobSpec
	for _, season := range seasons {
		rows, err := query(
			`SELECT vf.path FROM streamline_video_files vf
			JOIN streamline_episodes e ON e.id = vf.episode_id
			WHERE e.season_id = ? AND vf.missing = 0
			ORDER BY e.number, vf.id`, season)
		if err != nil {
			return nil, err
		}
		if len(rows.Rows) < 2 {
			continue
		}
		var paths []string
		var size, mtime int64
		for _, r := range rows.Rows {
			p := row(r).str(0)
			st, err := statHost(p)
			if err != nil {
				continue
			}
			paths = append(paths, p)
			size += st.size
			mtime = max(mtime, st.mtimeNs)
		}
		if len(paths) < 2 {
			continue
		}
		out = append(out, engineJobSpec{
			ID:    scan.JobID("intro", joinPaths(paths), size, mtime),
			Kind:  "intro",
			Path:  seasonKey(season),
			Paths: paths,
		})
	}
	return out, nil
}

// apply writes a page's changes to the record in one transaction.
func apply(
	st *scanState,
	changes []scan.Change,
	queued []engineJobSpec,
) (err error) {
	if err := exec(`BEGIN`); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = exec(`ROLLBACK`)
			return
		}
		err = exec(`COMMIT`)
	}()
	at := now().Unix()
	baseline, err := firstComparison()
	if err != nil {
		return err
	}
	var events []libraryEvent
	for _, c := range changes {
		f := c.File
		switch c.Kind {
		case scan.Gone:
			err = exec(
				`UPDATE seen SET gone_at = ? WHERE video_file_id = ?`,
				at,
				f.ID,
			)
			if err == nil {
				err = dropDerived(f.Path, f.ID)
			}
		case scan.New, scan.Replaced:
			if c.Previous != nil {
				err = dropDerived(c.Previous.Path, c.Previous.ID)
			}
			if err == nil {
				err = exec(
					`INSERT INTO seen(video_file_id, path, size, mtime_ns, movie_id,
						episode_id, seen_at, gone_at) VALUES(?, ?, ?, ?, ?, ?, ?, NULL)
					ON CONFLICT(video_file_id) DO UPDATE SET path = excluded.path,
						size = excluded.size, mtime_ns = excluded.mtime_ns,
						seen_at = excluded.seen_at, gone_at = NULL`,
					f.ID,
					f.Path,
					f.Size,
					strconv.FormatInt(f.MtimeNs, 10),
					nullable(f.MovieID),
					nullable(f.EpisodeID),
					at,
				)
			}
			if c.Kind == scan.New {
				st.added++
			} else {
				st.replaced++
			}
			if c.Kind == scan.New && baseline {
				continue
			}
			events = append(events, libraryEvent{
				Replaced: c.Kind == scan.Replaced, File: f, Previous: c.Previous,
			})
		}
		if err != nil {
			return err
		}
	}
	if err = recordJobs(queued); err != nil {
		return err
	}
	for _, e := range events {
		addEvent(e)
	}
	return nil
}

// firstComparison says whether no comparison has ever finished. What it
// finds is the library that was there before the extension, not something
// that arrived, so it is recorded without telling the activity feed.
func firstComparison() (bool, error) {
	res, err := query(
		`SELECT 1 FROM scan_state WHERE id = 1 AND last_at IS NULL`,
	)
	return err == nil && len(res.Rows) == 1, err
}

// dropDerived forgets what was worked out from a file that is gone or was
// replaced, so nothing describes bytes that are no longer there.
func dropDerived(path string, mediaID int64) error {
	if err := exec(`DELETE FROM analysis WHERE path = ?`, path); err != nil {
		return err
	}
	return exec(`DELETE FROM markers WHERE media_id = ?`, mediaID)
}

type libraryFolder struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Path  string      `json:"path"`
	Kind  string      `json:"kind"`
	Count int64       `json:"count"`
	Size  int64       `json:"size"`
	Scan  *folderScan `json:"scan,omitempty"`
}

type folderScan struct {
	Progress   float64 `json:"progress"`
	ETASeconds int64   `json:"eta_seconds"`
}

type lastScan struct {
	At       string `json:"at"`
	Added    int64  `json:"added"`
	Replaced int64  `json:"replaced"`
	Errors   int64  `json:"errors"`
}

type libraryView struct {
	Folders  []libraryFolder `json:"folders"`
	LastScan *lastScan       `json:"last_scan,omitempty"`
}

var libraryFolders = []struct {
	id, name, kind, titles, owner string
}{
	{"movies", "Movies", "movies", "streamline_movies", "movie_id"},
	{"series", "Series", "series", "streamline_series", "episode_id"},
}

var errCountFolder = errors.New("counting the files of a folder failed")

func adminLibrary(c *call) sdk.Response {
	rememberFolders(c.R)
	if st, err := loadScanState(); err == nil && !st.finished() {
		if _, err := compare(scanScope{}, compareBudget/2); err != nil {
			return libraryFailed(err)
		}
	}
	reachable := refreshJobs(collectBatch * 4)
	st, err := loadScanState()
	if err != nil {
		return libraryFailed(err)
	}
	active := scanActive(st, reachable)

	view := libraryView{Folders: []libraryFolder{}}
	for _, f := range libraryFolders {
		granted, ok := c.R.Folder(f.id)
		if !ok {
			continue
		}
		counts, err := query(
			`SELECT (SELECT count(*) FROM ` + f.titles + `),
				ifnull((SELECT sum(size) FROM streamline_video_files
					WHERE missing = 0 AND ` + f.owner + ` IS NOT NULL), 0)`)
		if err != nil || len(counts.Rows) == 0 {
			return libraryFailed(errCountFolder)
		}
		out := libraryFolder{
			ID: f.id, Name: f.name, Path: granted.HostPath, Kind: f.kind,
			Count: row(counts.Rows[0]).int(0), Size: row(counts.Rows[0]).int(1),
		}
		if active {
			progress, running, err := folderProgress(st, f.id)
			if err != nil {
				return libraryFailed(err)
			}
			if running {
				out.Scan = &progress
			}
		}
		view.Folders = append(view.Folders, out)
	}
	if last, err := query(
		`SELECT last_at, last_added, last_replaced, last_errors
		FROM scan_state WHERE id = 1 AND last_at IS NOT NULL`); err == nil &&
		len(last.Rows) == 1 {
		r := row(last.Rows[0])
		view.LastScan = &lastScan{
			At:    time.Unix(r.int(0), 0).UTC().Format("2006-01-02T15:04:05.000Z"),
			Added: r.int(1), Replaced: r.int(2), Errors: r.int(3),
		}
	}
	return jsonOK(http.StatusOK, view)
}

func jobsWaiting() int64 {
	res, err := query(`SELECT count(*) FROM jobs WHERE state = ?`, jobQueued)
	if err != nil || len(res.Rows) == 0 {
		return 0
	}
	return row(res.Rows[0]).int(0)
}

// scanActive says whether a scan is running: the comparison has rows left, or
// the engine has jobs to finish. Once it is over, the finished jobs are cleared.
func scanActive(st scanState, reachable bool) bool {
	res, err := query(
		`SELECT count(*), ifnull(sum(state = ?), 0) FROM jobs`, jobQueued)
	var total, waiting int64
	if err == nil && len(res.Rows) == 1 {
		total, waiting = row(res.Rows[0]).int(0), row(res.Rows[0]).int(1)
	}
	if !st.finished() || (reachable && waiting > 0) {
		return true
	}
	if total > 0 {
		_ = exec(`DELETE FROM jobs WHERE state != ?`, jobQueued)
	}
	return false
}

// folderProgress is how far the running scan is on one folder, and whether
// the folder has anything left: the rows of the comparison's scope there, the
// jobs queued for the folder's files, and the time left at the rate the scan
// went so far. The jobs count for the whole folder, not for the scope: a
// change to one title starts a comparison of its own while the engine still
// works through a full scan's jobs.
func folderProgress(st scanState, folder string) (folderScan, bool, error) {
	var compared, rows int64
	comparing := !st.finished() && st.scope.covers(folder)
	if comparing {
		cond, args := st.scope.only(folder).where()
		res, err := query(
			`SELECT ifnull(sum(id <= ?), 0), count(*) FROM streamline_video_files
			WHERE `+cond,
			append([]any{st.cursor}, args...)...)
		if err != nil {
			return folderScan{}, false, err
		}
		compared, rows = row(res.Rows[0]).int(0), row(res.Rows[0]).int(1)
	}
	// Every file job is recorded together with its file's row in seen, which
	// names the owner; intro jobs carry a season key instead of a path.
	cond, args := scanScope{Full: []string{folder}}.where()
	owned := `EXISTS (SELECT 1 FROM seen WHERE seen.path = jobs.path AND ` +
		cond + `)`
	if folder == "series" {
		owned = `(jobs.kind = 'intro' OR ` + owned + `)`
	}
	res, err := query(
		`SELECT ifnull(sum(state = ?), 0), count(*) FROM jobs WHERE `+owned,
		append([]any{jobQueued}, args...)...)
	if err != nil {
		return folderScan{}, false, err
	}
	waiting, jobs := row(res.Rows[0]).int(0), row(res.Rows[0]).int(1)
	p := scan.Progress(compared, rows, waiting, jobs)
	return folderScan{
		Progress:   math.Round(p*1000) / 1000,
		ETASeconds: scan.ETASeconds(now().Sub(time.Unix(st.startedAt, 0)), p),
	}, comparing || waiting > 0, nil
}

func adminScan(c *call) sdk.Response {
	rememberFolders(c.R)
	var body struct {
		FolderID string `json:"folder_id"`
	}
	if len(c.R.Body) > 0 {
		if err := decodeBody(c, &body); err != nil {
			return fail(http.StatusBadRequest, "invalid_request", "Invalid request")
		}
	}
	scope := scanScope{Full: []string{"movies", "series"}}
	if body.FolderID != "" {
		if body.FolderID != "movies" && body.FolderID != "series" {
			return libraryNotFound()
		}
		scope = scanScope{Full: []string{body.FolderID}}
	}
	if err := putSetting(callbackBaseKey, callbackBase); err != nil {
		return internalError("keeping the address for the engine", err)
	}
	done, err := compare(scope, compareBudget)
	if err != nil {
		return libraryFailed(err)
	}
	noteUnfinished(done)
	return jsonOK(http.StatusAccepted, struct{}{})
}

func noteUnfinished(done bool) {
	if !done {
		continueLater()
	}
}
