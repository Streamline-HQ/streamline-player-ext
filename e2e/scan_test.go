package e2e

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/e2e/harness"
	"github.com/streamline-hq/streamline-player-ext/scan"
)

type adminLibrary struct {
	Folders []struct {
		ID    string `json:"id"`
		Kind  string `json:"kind"`
		Count int    `json:"count"`
		Size  int64  `json:"size"`
		Scan  *struct {
			Progress   float64 `json:"progress"`
			ETASeconds int64   `json:"eta_seconds"`
		} `json:"scan"`
	} `json:"folders"`
	LastScan *struct {
		At       string `json:"at"`
		Added    int    `json:"added"`
		Replaced int    `json:"replaced"`
		Errors   int    `json:"errors"`
	} `json:"last_scan"`
}

func (l adminLibrary) scanning() bool {
	for _, f := range l.Folders {
		if f.Scan != nil {
			return true
		}
	}
	return false
}

func startScan(body any) {
	status, raw := call(
		http.MethodPost, "/admin/library/scan", key(stack.AdminKey), body,
	)
	ExpectWithOffset(1, status).To(Equal(http.StatusAccepted), string(raw))
}

func waitScanned() adminLibrary {
	var lib adminLibrary
	EventuallyWithOffset(1, func() bool {
		lib = adminLibrary{}
		getJSON("/admin/library", key(stack.AdminKey), &lib)
		return !lib.scanning() && lib.LastScan != nil
	}, 6*time.Minute, time.Second).Should(BeTrue(), scanDump)
	return lib
}

func scanDump() string {
	db := stack.Ext()
	defer db.Close()
	out := ""
	for _, q := range []string{
		`SELECT cursor || ' ' || scope || ' ' || ifnull(finished_at, 'open') ||
			' errors ' || errors || ' added ' || added || ' replaced ' || replaced || ' last ' || last_added || '/' || last_replaced FROM scan_state`,
		`SELECT kind || ' ' || state || ' ' || path FROM jobs`,
		`SELECT video_file_id || ' ' || size || ' ' || mtime_ns || ' ' ||
			ifnull(gone_at, '-') || ' ' || path FROM seen`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			return err.Error()
		}
		for rows.Next() {
			var line string
			_ = rows.Scan(&line)
			out += line + "\n"
		}
		rows.Close()
	}
	req, _ := http.NewRequest(http.MethodGet, stack.EngineURL+"/v1/jobs", nil)
	req.Header.Set("Authorization", "Bearer "+stack.EngineSecret)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		out += "engine: " + string(raw) + "\n"
	}
	return out
}

func engineJob(id string) (int, string) {
	req, err := http.NewRequest(
		http.MethodGet, stack.EngineURL+"/v1/jobs/"+id, nil,
	)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	req.Header.Set("Authorization", "Bearer "+stack.EngineSecret)
	resp, err := http.DefaultClient.Do(req)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	var st struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(raw, &st)
	return resp.StatusCode, st.State
}

func jobIDOf(kind, path string) string {
	info, err := os.Stat(path)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return scan.JobID(kind, path, info.Size(), info.ModTime().UnixNano())
}

func storedAnalyses() map[string]int {
	db := stack.Ext()
	defer db.Close()
	rows, err := db.Query(`SELECT path, version FROM analysis`)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var p string
		var v int
		ExpectWithOffset(1, rows.Scan(&p, &v)).To(Succeed())
		out[p] = v
	}
	return out
}

// seasonOf is the season of an episode, with the seeded files of its
// episodes in the order the comparison lists them.
func seasonOf(episode harness.SeededFile) (int64, []harness.SeededFile) {
	GinkgoHelper()
	seasonID := func(f harness.SeededFile) int64 {
		var id int64
		Expect(stack.Core.QueryRow(
			`SELECT season_episodes FROM episodes WHERE id = ?`, f.OwnerID,
		).Scan(&id)).To(Succeed())
		return id
	}
	id := seasonID(episode)
	var files []harness.SeededFile
	for _, e := range stack.Seeded.Episodes {
		if seasonID(e) == id {
			files = append(files, e)
		}
	}
	return id, files
}

func introJobIDOf(files []harness.SeededFile) string {
	GinkgoHelper()
	paths := make([]string, 0, len(files))
	var size, mtime int64
	for _, f := range files {
		info, err := os.Stat(f.Path)
		Expect(err).NotTo(HaveOccurred())
		paths = append(paths, f.Path)
		size += info.Size()
		mtime = max(mtime, info.ModTime().UnixNano())
	}
	return scan.JobID("intro", strings.Join(paths, "\n"), size, mtime)
}

// storedIntros is the media ids of a season that have an intro stored.
func storedIntros(seasonKey string) []int64 {
	GinkgoHelper()
	db := stack.Ext()
	defer db.Close()
	rows, err := db.Query(
		`SELECT media_id FROM markers
		WHERE season_key = ? AND intro_start IS NOT NULL ORDER BY media_id`,
		seasonKey,
	)
	Expect(err).NotTo(HaveOccurred())
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		Expect(rows.Scan(&id)).To(Succeed())
		ids = append(ids, id)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	return ids
}

// keepRows saves the rows of an extension table that match cond and puts them
// back when the spec ends.
func keepRows(table, cond string, args ...any) {
	GinkgoHelper()
	db := stack.Ext()
	defer db.Close()
	rows, err := db.Query(`SELECT * FROM `+table+` WHERE `+cond, args...)
	Expect(err).NotTo(HaveOccurred())
	defer rows.Close()
	cols, err := rows.Columns()
	Expect(err).NotTo(HaveOccurred())
	var kept [][]any
	for rows.Next() {
		r := make([]any, len(cols))
		into := make([]any, len(cols))
		for i := range r {
			into[i] = &r[i]
		}
		Expect(rows.Scan(into...)).To(Succeed())
		kept = append(kept, r)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	insert := `INSERT OR REPLACE INTO ` + table + `(` + strings.Join(cols, ", ") +
		`) VALUES(` + strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + `)`
	DeferCleanup(func() {
		for _, r := range kept {
			extExec(insert, r...)
		}
	})
}

func restoreFile(f harness.SeededFile) {
	var current string
	ExpectWithOffset(1, stack.Core.QueryRow(
		`SELECT path FROM media_files WHERE id = ?`, f.FileID,
	).Scan(&current)).To(Succeed())
	if current == f.Path {
		return
	}
	ExpectWithOffset(1, os.Rename(current, f.Path)).To(Succeed())
	_, err := stack.Core.Exec(
		`UPDATE media_files SET path = ? WHERE id = ?`, f.Path, f.FileID,
	)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

var _ = Describe("library comparison", Label("e2e"), Ordered, func() {
	It("is 404 from a client on the hook route", func() {
		status, _ := call(http.MethodPost, "/hooks/library", nil, map[string]any{
			"full": []string{"movies"},
		})
		Expect(status).To(Equal(http.StatusNotFound))
	})

	It("takes a scan from an admin only, and 404s an unknown folder", func() {
		user := stack.NewUser("scanner")
		k := stack.SignInApp(user, "scan-app")
		status, _ := call(http.MethodPost, "/admin/library/scan", key(k), nil)
		Expect(status).To(Equal(http.StatusForbidden))
		status, raw := call(
			http.MethodPost, "/admin/library/scan", key(stack.AdminKey),
			map[string]string{"folder_id": "music"},
		)
		Expect(status).To(Equal(http.StatusNotFound), string(raw))
	})

	It("queues analyze, subtitles and trickplay, and keeps the analysis", func() {
		startScan(map[string]string{})
		lib := waitScanned()
		Expect(lib.Folders).To(HaveLen(2))

		analyses := storedAnalyses()
		for _, f := range append(
			append([]harness.SeededFile{}, stack.Seeded.Movies...),
			stack.Seeded.Episodes...,
		) {
			Expect(analyses).To(HaveKey(f.Path))
			for _, kind := range []string{"subtitles", "trickplay"} {
				status, state := engineJob(jobIDOf(kind, f.Path))
				Expect(status).To(Equal(http.StatusOK), kind+" "+f.Path)
				Expect(state).To(Equal("done"))
			}
			status, _ := engineJob(jobIDOf("analyze", f.Path))
			Expect(status).To(Equal(http.StatusNotFound), "collected and released")
		}
	})

	It("stores what the engine pushes, with nobody on the Library page", func() {
		film := stack.Seeded.Movies[0]
		keepRows("analysis", "path = ?", film.Path)
		extExec(`DELETE FROM analysis WHERE path = ?`, film.Path)
		startScan(map[string]string{})
		Eventually(storedAnalyses, time.Minute, 250*time.Millisecond).
			Should(HaveKey(film.Path))
		Eventually(func() int {
			status, _ := engineJob(jobIDOf("analyze", film.Path))
			return status
		}, 10*time.Second, 250*time.Millisecond).
			Should(Equal(http.StatusNotFound), "released once stored")
	})

	It("looks at a movie again when Streamline says it changed", func() {
		film := stack.Seeded.Movies[0]
		keepRows("analysis", "path = ?", film.Path)
		extExec(`DELETE FROM analysis WHERE path = ?`, film.Path)
		status, raw, err := stack.Do(
			http.MethodPatch,
			fmt.Sprintf("/api/v1/movies/%d", film.OwnerID),
			stack.AdminToken,
			map[string]any{"monitored": false},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusOK), string(raw))
		Eventually(storedAnalyses, time.Minute, 250*time.Millisecond).
			Should(HaveKey(film.Path), "no player request was made")
	})

	It("finishes a scan with nobody connected", func() {
		// Another request holds the comparison for a few seconds, so the scan
		// request ends with the work still to do.
		extExec(
			`INSERT INTO meta(key, value) VALUES('scan_lease', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			time.Now().Add(3*time.Second).Unix(),
		)
		began := time.Now().Unix()
		startScan(map[string]string{})
		Eventually(func() int64 {
			db := stack.Ext()
			defer db.Close()
			var finished sql.NullInt64
			Expect(db.QueryRow(
				`SELECT finished_at FROM scan_state WHERE id = 1`,
			).Scan(&finished)).To(Succeed())
			return finished.Int64
		}, 30*time.Second, 250*time.Millisecond).
			Should(BeNumerically(">=", began), "the engine carried it on")
	})

	It("serves the stored analysis and markers through tracks", func() {
		k := stack.SignInApp(stack.NewUser("tracks"), "tracks-app")
		episode := stack.Seeded.Episodes[0]
		keepRows("markers", "media_id = ?", episode.FileID)
		extExec(`DELETE FROM markers WHERE media_id = ?`, episode.FileID)
		extExec(
			`INSERT INTO markers(media_id, season_key, intro_start, intro_end)
			VALUES(?, 'season:e2e', 3, 7.5)`, episode.FileID,
		)
		DeferCleanup(extExec, `DELETE FROM markers WHERE season_key = 'season:e2e'`)
		var tracks struct {
			Markers struct {
				Intro   *[2]float64 `json:"intro"`
				Credits *[2]float64 `json:"credits"`
			} `json:"markers"`
		}
		getJSON(
			"/playback/media/"+strconv.FormatInt(episode.FileID, 10)+"/tracks",
			key(k), &tracks,
		)
		Expect(tracks.Markers.Intro).NotTo(BeNil())
		Expect(*tracks.Markers.Intro).To(Equal([2]float64{3, 7.5}))
		Expect(tracks.Markers.Credits).To(BeNil())

		film := stack.Seeded.Movies[0]
		getJSON(
			"/playback/media/"+strconv.FormatInt(film.FileID, 10)+"/tracks",
			key(k), &tracks,
		)
		status, _ := engineJob(jobIDOf("analyze", film.Path))
		Expect(
			status,
		).To(Equal(http.StatusNotFound), "no analysis was asked for again")
	})

	It("leaves a running comparison to the request that holds it", func() {
		scope := `{"full":["movies"]}`
		extExec(
			`INSERT INTO scan_state(id, cursor, scope, started_at, finished_at)
			VALUES(1, 5, ?, ?, NULL) ON CONFLICT(id) DO UPDATE SET cursor = 5,
				scope = excluded.scope, started_at = excluded.started_at,
				finished_at = NULL`,
			scope, time.Now().Unix(),
		)
		extExec(
			`INSERT INTO meta(key, value) VALUES('scan_lease', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10),
		)
		DeferCleanup(func() {
			extExec(`UPDATE meta SET value = '0' WHERE key = 'scan_lease'`)
			startScan(nil)
			waitScanned()
		})
		cursor := func() (int64, string, bool) {
			db := stack.Ext()
			defer db.Close()
			var c int64
			var sc string
			var finished bool
			ExpectWithOffset(1, db.QueryRow(
				`SELECT cursor, scope, finished_at IS NOT NULL FROM scan_state`,
			).Scan(&c, &sc, &finished)).To(Succeed())
			return c, sc, finished
		}

		var lib adminLibrary
		getJSON("/admin/library", key(stack.AdminKey), &lib)
		c, _, finished := cursor()
		Expect(c).To(BeEquivalentTo(5))
		Expect(finished).To(BeFalse())

		startScan(map[string]string{"folder_id": "movies"})
		c, _, finished = cursor()
		Expect(c).To(BeEquivalentTo(5), "a folder already scanning is left alone")
		Expect(finished).To(BeFalse())

		startScan(map[string]string{"folder_id": "series"})
		c, sc, _ := cursor()
		Expect(c).To(BeZero())
		Expect(sc).To(ContainSubstring("series"))
		Expect(sc).To(ContainSubstring("movies"))
	})

	It(
		"reports each folder's own progress, and the time left at the pace of the last jobs",
		func() {
			movies := make([]int64, 0, len(stack.Seeded.Movies))
			for _, m := range stack.Seeded.Movies {
				movies = append(movies, m.FileID)
			}
			slices.Sort(movies)
			Expect(len(movies)).To(BeNumerically(">=", 2))
			episodes := make([]int64, 0, len(stack.Seeded.Episodes))
			for _, e := range stack.Seeded.Episodes {
				episodes = append(episodes, e.FileID)
			}
			Expect(slices.Min(episodes)).To(BeNumerically(">", movies[0]))

			// Three movie jobs finished half a minute apart, the last one just now,
			// and three wait: 90 s. An episode's job waits behind them all: 120 s.
			span := time.Minute
			film := stack.Seeded.Movies[0]
			extExec(`DELETE FROM job_ends`)
			extExec(`DELETE FROM jobs`)
			for i, state := range []string{"done", "done", "done", "queued", "queued", "queued"} {
				id := fmt.Sprintf("eta-%d", i)
				extExec(
					`INSERT INTO jobs(job_id, kind, path, state)
				VALUES(?, 'trickplay', ?, ?)`, id, film.Path, state)
				if state == "done" {
					extExec(
						`INSERT INTO job_ends(job_id, at) VALUES(?, ?)`,
						id, time.Now().Add(-span*time.Duration(2-i)/2).Unix())
				}
			}
			extExec(
				`INSERT INTO jobs(job_id, kind, path, state)
			VALUES('eta-last', 'trickplay', ?, 'queued')`,
				stack.Seeded.Episodes[0].Path)
			extExec(
				`INSERT INTO scan_state(id, cursor, scope, started_at, finished_at)
			VALUES(1, ?, ?, ?, NULL) ON CONFLICT(id) DO UPDATE SET
				cursor = excluded.cursor, scope = excluded.scope,
				started_at = excluded.started_at, finished_at = NULL`,
				movies[0], `{"full":["movies","series"]}`,
				time.Now().Add(-time.Hour).Unix(),
			)
			extExec(
				`INSERT INTO meta(key, value) VALUES('scan_lease', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
				strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10),
			)
			DeferCleanup(func() {
				extExec(`DELETE FROM job_ends WHERE job_id LIKE 'eta-%'`)
				extExec(`DELETE FROM jobs WHERE job_id LIKE 'eta-%'`)
				extExec(`UPDATE meta SET value = '0' WHERE key = 'scan_lease'`)
				startScan(nil)
				waitScanned()
			})

			var lib adminLibrary
			getJSON("/admin/library", key(stack.AdminKey), &lib)
			progress := map[string]float64{}
			for _, f := range lib.Folders {
				Expect(f.Scan).NotTo(BeNil(), f.ID)
				p := f.Scan.Progress
				progress[f.ID] = p
				Expect(p).To(BeNumerically("<", 1), f.ID)
				if f.ID == "series" {
					Expect(f.Scan.ETASeconds).To(
						BeNumerically("~", 2*span.Seconds(), 4),
						"its one job waits behind the movies' three")
					continue
				}
				Expect(f.Scan.ETASeconds).To(
					BeNumerically("~", 3*span.Seconds()/2, 4),
					"not the hour since the scan began")
			}
			Expect(progress).To(HaveLen(2))
			Expect(progress["movies"]).To(
				BeNumerically(">", progress["series"]),
				"movies are partly compared, series not yet",
			)
		},
	)

	It("keeps a scan's progress when one title changes meanwhile", func() {
		film, episode := stack.Seeded.Movies[0], stack.Seeded.Episodes[0]
		lib := waitScanned()
		// The engine holds these for real: jobs it does not owe are dropped
		// from the record. There are enough to still be waiting afterwards.
		const each = 200
		ids := make([]string, 0, 2*each)
		batch := make([]map[string]any, 0, 2*each)
		for i := range each {
			for n, path := range []string{film.Path, episode.Path} {
				id := fmt.Sprintf("%032x", 0xf0000+i*2+n)
				ids = append(ids, id)
				batch = append(batch, map[string]any{
					"job_id": id, "type": "trickplay", "priority": "background",
					"path": path,
				})
				extExec(
					`INSERT INTO jobs(job_id, kind, path, state)
					VALUES(?, 'trickplay', ?, 'queued')`,
					id, path,
				)
			}
		}
		engineCall := func(method, path string, body any) int {
			raw, err := json.Marshal(body)
			Expect(err).NotTo(HaveOccurred())
			req, err := http.NewRequest(
				method, stack.EngineURL+path, bytes.NewReader(raw),
			)
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Authorization", "Bearer "+stack.EngineSecret)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Body.Close()).To(Succeed())
			return resp.StatusCode
		}
		Expect(engineCall(http.MethodPost, "/v1/jobs", batch)).To(
			BeNumerically("<", http.StatusBadRequest),
		)
		DeferCleanup(func() {
			for _, id := range ids {
				engineCall(http.MethodDelete, "/v1/jobs/"+id, nil)
			}
			extExec(
				`DELETE FROM jobs WHERE job_id LIKE '000000000000000000000000000f%'`,
			)
			waitScanned()
		})

		status, raw, err := stack.Do(
			http.MethodPatch,
			fmt.Sprintf("/api/v1/movies/%d", film.OwnerID),
			stack.AdminToken,
			map[string]any{"monitored": true},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusOK), string(raw))
		Eventually(func() bool {
			db := stack.Ext()
			defer db.Close()
			var scope string
			var finished bool
			ExpectWithOffset(1, db.QueryRow(
				`SELECT scope, finished_at IS NOT NULL FROM scan_state`,
			).Scan(&scope, &finished)).To(Succeed())
			return strings.Contains(scope, `"movies":[`) && finished
		}, time.Minute, 250*time.Millisecond).Should(BeTrue())

		var now adminLibrary
		getJSON("/admin/library", key(stack.AdminKey), &now)
		for _, f := range now.Folders {
			Expect(f.Scan).NotTo(BeNil(), f.ID)
			Expect(f.Scan.Progress).To(BeNumerically("<", 1), f.ID)
		}
		Expect(now.Folders).To(HaveLen(2))
		Expect(now.LastScan).To(Equal(lib.LastScan), "the last scan is the full one")
	})

	It("drops what an analyze job for older bytes found", func() {
		film := stack.Seeded.Movies[0]
		var kept int
		db := stack.Ext()
		Expect(db.QueryRow(
			`SELECT version FROM analysis WHERE path = ?`, film.Path,
		).Scan(&kept)).To(Succeed())
		Expect(db.Close()).To(Succeed())
		extExec(`UPDATE analysis SET version = -1 WHERE path = ?`, film.Path)
		DeferCleanup(
			extExec,
			`UPDATE analysis SET version = ? WHERE path = ?`,
			kept,
			film.Path,
		)

		old := fmt.Sprintf("%x", sha256.Sum256([]byte("older bytes")))[:32]
		body, err := json.Marshal([]map[string]any{{
			"job_id": old, "type": "analyze", "priority": "interactive",
			"path":     film.Path,
			"callback": stack.BaseURL + "/extensions/player/engine/jobs/" + old,
		}})
		Expect(err).NotTo(HaveOccurred())
		extExec(
			`INSERT INTO jobs(job_id, kind, path, state) VALUES(?, 'analyze', ?, 'queued')`,
			old,
			film.Path,
		)
		req, err := http.NewRequest(
			http.MethodPost, stack.EngineURL+"/v1/jobs", bytes.NewReader(body),
		)
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Authorization", "Bearer "+stack.EngineSecret)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusAccepted))
		// The engine posts the result, and the job is released once taken.
		Eventually(func() int {
			status, _ := engineJob(old)
			return status
		}, time.Minute, 250*time.Millisecond).Should(Equal(http.StatusNotFound))
		Expect(storedAnalyses()).To(HaveKeyWithValue(film.Path, -1))
	})

	It("submits again the jobs a replaced engine lost", func() {
		film := stack.Seeded.Movies[0]
		var version int
		var analysis string
		db := stack.Ext()
		Expect(db.QueryRow(
			`SELECT version, json FROM analysis WHERE path = ?`, film.Path,
		).Scan(&version, &analysis)).To(Succeed())
		Expect(db.Close()).To(Succeed())
		extExec(`DELETE FROM analysis WHERE path = ?`, film.Path)
		DeferCleanup(
			extExec,
			`INSERT INTO analysis(path, version, json) VALUES(?, ?, ?)
			ON CONFLICT(path) DO NOTHING`,
			film.Path, version, analysis,
		)

		startScan(map[string]string{"folder_id": "movies"})
		lost := make([]string, 0, 3)
		for _, kind := range []string{"analyze", "subtitles", "trickplay"} {
			lost = append(lost, jobIDOf(kind, film.Path))
		}
		Expect(stack.ReplaceEngine(lost...)).To(Succeed())
		status, _ := engineJob(lost[0])
		Expect(status).To(Equal(http.StatusNotFound))

		startScan(map[string]string{"folder_id": "movies"})
		for _, id := range lost {
			status, _ := engineJob(id)
			Expect(status).To(Equal(http.StatusOK), scanDump)
		}
		waitScanned()
		Expect(storedAnalyses()).To(HaveKeyWithValue(film.Path, version))
	})

	It("checks a season's intro again only while its files went unchecked", func() {
		seasonID, files := seasonOf(stack.Seeded.Episodes[0])
		Expect(len(files)).To(BeNumerically(">=", 2))
		seasonKey := "season:" + strconv.FormatInt(seasonID, 10)
		want := make([]int64, 0, len(files))
		for _, f := range files {
			want = append(want, f.FileID)
		}
		slices.Sort(want)
		keepRows("markers", "season_key = ?", seasonKey)
		keepRows("intro_checked", "season_key = ?", seasonKey)
		extExec(`DELETE FROM markers WHERE season_key = ?`, seasonKey)
		extExec(`DELETE FROM intro_checked WHERE season_key = ?`, seasonKey)

		intro := introJobIDOf(files)
		Expect(stack.ReplaceEngine(intro)).To(Succeed())
		status, _ := engineJob(intro)
		Expect(status).To(Equal(http.StatusNotFound))

		startScan(map[string]string{"folder_id": "series"})
		status, _ = engineJob(intro)
		Expect(status).To(Equal(http.StatusOK), scanDump)
		waitScanned()
		Expect(storedIntros(seasonKey)).To(Equal(want), scanDump)

		By("leaving alone a season checked as it is, where no intro was found")
		extExec(`DELETE FROM markers WHERE season_key = ?`, seasonKey)
		startScan(map[string]string{"folder_id": "series"})
		status, _ = engineJob(intro)
		Expect(status).To(Equal(http.StatusNotFound), scanDump)
		waitScanned()
		Expect(storedIntros(seasonKey)).To(BeEmpty())
	})

	It("queues a replacement and drops the old analysis", func() {
		old := stack.Seeded.Movies[len(stack.Seeded.Movies)-1]
		DeferCleanup(restoreFile, old)
		raw, err := os.ReadFile(old.Path)
		Expect(err).NotTo(HaveOccurred())
		fresh := filepath.Join(filepath.Dir(old.Path), "replacement.mkv")
		Expect(os.WriteFile(fresh, raw, 0o644)).To(Succeed())
		Expect(os.Remove(old.Path)).To(Succeed())
		_, err = stack.Core.Exec(
			`UPDATE media_files SET path = ?, size = ? WHERE id = ?`,
			fresh, int64(len(raw)), old.FileID,
		)
		Expect(err).NotTo(HaveOccurred())

		startScan(map[string]string{"folder_id": "movies"})
		lib := waitScanned()
		Expect(lib.LastScan.Replaced).To(Equal(1), scanDump)

		analyses := storedAnalyses()
		Expect(analyses).NotTo(HaveKey(old.Path))
		Expect(analyses).To(HaveKey(fresh))
		status, state := engineJob(jobIDOf("subtitles", fresh))
		Expect(status).To(Equal(http.StatusOK))
		Expect(state).To(Equal("done"))
	})
})
