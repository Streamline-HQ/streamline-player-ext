package e2e

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/e2e/harness"
)

type downloadJob struct {
	ID        string  `json:"id"`
	MediaID   string  `json:"media_id"`
	State     string  `json:"state"`
	Progress  float64 `json:"progress"`
	Size      *int64  `json:"size"`
	Container *string `json:"container"`
	URL       *string `json:"url"`
}

func downloadBody(mediaID int64, quality string, audio ...int) map[string]any {
	return map[string]any{
		"media_id":      strconv.FormatInt(mediaID, 10),
		"quality":       quality,
		"audio_indexes": audio,
		"capabilities":  mp4Only,
	}
}

func createDownload(k string, body any) (int, downloadJob, []byte) {
	status, raw := call(http.MethodPost, "/downloads", key(k), body)
	var job downloadJob
	_ = json.Unmarshal(raw, &job)
	return status, job, raw
}

func downloadOf(k, id string) (int, downloadJob) {
	status, raw := call(http.MethodGet, "/downloads/"+id, key(k), nil)
	var job downloadJob
	_ = json.Unmarshal(raw, &job)
	return status, job
}

var _ = Describe("downloads", Label("e2e"), Ordered, func() {
	var (
		user     harness.User
		k, other string
		film     int64
	)

	BeforeAll(func() {
		user = stack.NewUser("downloads")
		k = stack.SignInApp(user, "downloads-app")
		other = stack.SignInApp(
			stack.NewUser("downloads-other"),
			"downloads-other-app",
		)
		film = stack.Seeded.Movies[0].FileID
	})

	It(
		"a download goes ready, is fetched from the engine with its signed URL, and completes",
		func() {
			status, job, raw := createDownload(k, downloadBody(film, "original", 1))
			Expect(status).To(Equal(http.StatusCreated), string(raw))
			Expect(job.ID).To(MatchRegexp(`^[0-9a-f]{32}$`))
			Expect(job.MediaID).To(Equal(strconv.FormatInt(film, 10)))
			DeferCleanup(func() {
				call(http.MethodDelete, "/downloads/"+job.ID, key(k), nil)
			})

			again, same, _ := createDownload(k, downloadBody(film, "original", 1, 1))
			Expect(again).To(Equal(http.StatusCreated))
			Expect(same.ID).To(Equal(job.ID))

			var ready downloadJob
			Eventually(func() string {
				var st int
				st, ready = downloadOf(k, job.ID)
				Expect(st).To(Equal(http.StatusOK))
				return ready.State
			}, 2*time.Minute, 500*time.Millisecond).Should(Equal("ready"))
			Expect(ready.Progress).To(BeNumerically("==", 1))
			Expect(ready.Container).NotTo(BeNil())
			Expect(ready.Size).NotTo(BeNil())
			Expect(ready.URL).NotTo(BeNil())
			Expect(
				*ready.URL,
			).To(HavePrefix(stack.EngineURL + "/dl/" + job.ID + "?exp="))

			status, body := get(*ready.URL)
			Expect(status).To(Equal(http.StatusOK))
			Expect(int64(len(body))).To(Equal(*ready.Size))

			before := activityIDs(activityOf(key(k)))
			status, raw = call(
				http.MethodPost,
				"/downloads/"+job.ID+"/complete",
				key(k),
				nil,
			)
			Expect(status).To(Equal(http.StatusNoContent), string(raw))

			status, _ = downloadOf(k, job.ID)
			Expect(status).To(Equal(http.StatusNotFound))
			var made *activityItem
			for _, it := range activityOf(key(k)) {
				if !before[it.ID] && it.Kind == "download" {
					made = &it
					break
				}
			}
			Expect(made).NotTo(BeNil())
			Expect(made.Read).To(BeFalse())
			Expect(made.Title).NotTo(BeNil())
			Expect(made.Device).NotTo(BeNil())
			Expect(made.Device.Name).NotTo(BeEmpty())
			for _, it := range activityOf(key(other)) {
				Expect(it.Kind).NotTo(Equal("download"), "only its maker sees it")
			}
		},
	)

	It("a bearer is 403, another device's job 404", func() {
		stack.SetSetting("player.origins", []string{webOrigin})
		browser := stack.SignInBrowser(stack.NewUser("downloads-web"), webOrigin)
		for _, c := range []struct{ method, path string }{
			{http.MethodPost, "/downloads"},
			{http.MethodGet, "/downloads/" + "0123456789abcdef0123456789abcdef"},
			{http.MethodDelete, "/downloads/" + "0123456789abcdef0123456789abcdef"},
			{http.MethodPost, "/downloads/0123456789abcdef0123456789abcdef/complete"},
		} {
			status, raw := call(
				c.method,
				c.path,
				bearer(browser),
				downloadBody(film, "original", 1),
			)
			Expect(status).To(Equal(http.StatusForbidden), c.method+" "+c.path)
			Expect(errorCode(raw)).To(Equal("forbidden"))
		}

		status, job, raw := createDownload(k, downloadBody(film, "720p", 1))
		Expect(status).To(Equal(http.StatusCreated), string(raw))
		DeferCleanup(func() {
			call(http.MethodDelete, "/downloads/"+job.ID, key(k), nil)
		})
		status, raw = call(http.MethodGet, "/downloads/"+job.ID, key(other), nil)
		Expect(status).To(Equal(http.StatusNotFound))
		Expect(errorCode(raw)).To(Equal("not_found"))
		status, _ = call(
			http.MethodPost,
			"/downloads/"+job.ID+"/complete",
			key(other),
			nil,
		)
		Expect(status).To(Equal(http.StatusNotFound))
		status, _ = call(http.MethodDelete, "/downloads/"+job.ID, key(other), nil)
		Expect(status).To(Equal(http.StatusNoContent))
		status, _ = downloadOf(k, job.ID)
		Expect(status).To(Equal(http.StatusOK), "the other device deleted nothing")
	})

	It(
		"refuses a bad request, an unknown medium and an audio track the file lacks",
		func() {
			status, raw := createDownload2(k, map[string]any{"media_id": "1"})
			Expect(status).To(Equal(http.StatusBadRequest), string(raw))
			Expect(errorCode(raw)).To(Equal("invalid_request"))
			status, raw = createDownload2(k, downloadBody(film, "4k", 1))
			Expect(status).To(Equal(http.StatusBadRequest), string(raw))
			status, raw = createDownload2(k, downloadBody(film, "original"))
			Expect(status).To(Equal(http.StatusBadRequest), string(raw))
			status, raw = createDownload2(k, downloadBody(film, "original", 99))
			Expect(status).To(Equal(http.StatusBadRequest), string(raw))
			status, raw = createDownload2(k, downloadBody(999999, "original", 1))
			Expect(status).To(Equal(http.StatusNotFound), string(raw))
			Expect(errorCode(raw)).To(Equal("not_found"))
		},
	)

	It("completes only a ready job, and deleting an unknown one is 204", func() {
		const lost = "fedcba9876543210fedcba9876543210"
		extExec(`INSERT INTO downloads(id, device_id, user_id, media_id)
			SELECT ?, d.id, d.user_id, ? FROM devices d
			WHERE d.user_id = ? AND d.kind = 'app'`, lost, film, user.ID)
		status, raw := call(
			http.MethodPost,
			"/downloads/"+lost+"/complete",
			key(k),
			nil,
		)
		Expect(status).To(Equal(http.StatusConflict), string(raw))
		Expect(errorCode(raw)).To(Equal("not_ready"))
		_, job := downloadOf(k, lost)
		Expect(job.State).To(Equal("failed"))

		status, _ = call(http.MethodDelete, "/downloads/"+lost, key(k), nil)
		Expect(status).To(Equal(http.StatusNoContent))
		status, _ = call(http.MethodDelete, "/downloads/"+lost, key(k), nil)
		Expect(status).To(Equal(http.StatusNoContent))
	})

	It("an older offline watch body is ignored", func() {
		media := stack.Seeded.Movies[1].FileID
		path := "/watchstate/" + strconv.FormatInt(media, 10)
		put := func(position int, at string) (int, map[string]any) {
			status, raw := call(http.MethodPut, path, key(k), map[string]any{
				"position_seconds": position, "duration_seconds": 200, "at": at,
			})
			var out map[string]any
			_ = json.Unmarshal(raw, &out)
			return status, out
		}

		status, st := put(100, "2026-01-01T00:00:00Z")
		Expect(status).To(Equal(http.StatusOK))
		Expect(st["position_seconds"]).To(BeNumerically("==", 100))
		Expect(st["watched"]).To(BeFalse())

		status, st = put(5, "2025-12-31T00:00:00Z")
		Expect(status).To(Equal(http.StatusOK))
		Expect(st["position_seconds"]).To(BeNumerically("==", 100), "the state held")

		status, st = put(190, "2026-01-02T00:00:00Z")
		Expect(status).To(Equal(http.StatusOK))
		Expect(st["position_seconds"]).To(BeNumerically("==", 190))
		Expect(st["watched"]).To(BeTrue())

		status, _ = put(1, "yesterday")
		Expect(status).To(Equal(http.StatusBadRequest))
		status, _ = call(http.MethodPut, path, key(k), map[string]any{
			"position_seconds": -1,
			"duration_seconds": 200,
			"at":               "2026-01-03T00:00:00Z",
		})
		Expect(status).To(Equal(http.StatusBadRequest))
	})
})

func createDownload2(k string, body any) (int, []byte) {
	status, _, raw := createDownload(k, body)
	return status, raw
}
