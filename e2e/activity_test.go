package e2e

import (
	"net/http"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type activityItem struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	At    string `json:"at"`
	Read  bool   `json:"read"`
	Title *struct {
		Kind string `json:"kind"`
		ID   int64  `json:"id"`
	} `json:"title"`
	File *struct {
		Size int64 `json:"size"`
	} `json:"file"`
	Name     string  `json:"name"`
	Episodes []int64 `json:"episodes"`
	Device   *struct {
		Name     string `json:"name"`
		Platform string `json:"platform"`
	} `json:"device"`
}

// addFilmFile gives a film a second file: a hard link of its own, recorded in
// core as the first is. Both go when the spec ends.
func addFilmFile(path string, fileID int64, name string) (extra string, id int64) {
	GinkgoHelper()
	extra = filepath.Join(filepath.Dir(path), name)
	Expect(os.Link(path, extra)).To(Succeed())
	// RemoveAll: a spec may have deleted it already.
	DeferCleanup(os.RemoveAll, extra)
	res, err := stack.Core.Exec(
		`INSERT INTO media_files(create_time, update_time, path, size, quality,
			format, source, container, duration_seconds, video_codec, width,
			height, audio_codec, audio_channels, bitrate, audio_tracks,
			audio_langs, sub_langs, probed_at, bit_depth, tag_failures, movie_media_files)
		SELECT create_time, update_time, ?, size, quality, format, source,
			container, duration_seconds, video_codec, width, height,
			audio_codec, audio_channels, bitrate, audio_tracks, audio_langs,
			sub_langs, probed_at, bit_depth, tag_failures, movie_media_files
		FROM media_files WHERE id = ?`, extra, fileID)
	Expect(err).NotTo(HaveOccurred())
	id, err = res.LastInsertId()
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		_, err := stack.Core.Exec(`DELETE FROM media_files WHERE id = ?`, id)
		Expect(err).NotTo(HaveOccurred())
	})
	return extra, id
}

func activityOf(headers map[string]string) []activityItem {
	var feed struct {
		Items []activityItem `json:"items"`
	}
	getJSON("/activity", headers, &feed)
	return feed.Items
}

func activityIDs(items []activityItem) map[string]bool {
	ids := map[string]bool{}
	for _, it := range items {
		ids[it.ID] = true
	}
	return ids
}

var _ = Describe("activity", Label("e2e"), Ordered, func() {
	var k, other string

	BeforeAll(func() {
		k = stack.SignInApp(stack.NewUser("activity"), "activity-app")
		other = stack.SignInApp(
			stack.NewUser("activity-other"),
			"activity-other-app",
		)
	})

	It("the first comparison of a library already there is a baseline", func() {
		extExec(`DELETE FROM scan_state`)
		extExec(`DELETE FROM seen`)
		before := len(activityOf(key(k)))
		startScan(nil)
		waitScanned()
		Expect(activityOf(key(k))).To(HaveLen(before), scanDump())
		db := stack.Ext()
		defer db.Close()
		var seen int
		Expect(db.QueryRow(`SELECT count(*) FROM seen`).Scan(&seen)).To(Succeed())
		Expect(seen).To(BeNumerically(">", 0))
	})

	It("a scan's new film appears in activity, and read marks it", func() {
		before := activityIDs(activityOf(key(k)))
		film := stack.Seeded.Movies[0]
		addFilmFile(film.Path, film.FileID, "activity-extra.mkv")

		startScan(map[string]string{"folder_id": "movies"})
		waitScanned()

		var arrived *activityItem
		for _, it := range activityOf(key(k)) {
			if !before[it.ID] && it.Kind == "movie" && it.Title != nil &&
				it.Title.ID == film.OwnerID {
				arrived = &it
				break
			}
		}
		Expect(arrived).NotTo(BeNil(), scanDump())
		Expect(arrived.Title.Kind).To(Equal("movies"))
		Expect(arrived.Read).To(BeFalse())
		Expect(arrived.File).NotTo(BeNil())
		Expect(arrived.File.Size).To(BeNumerically(">", 0))

		status, raw := call(http.MethodPost, "/activity/read", key(k),
			map[string]any{"ids": []string{arrived.ID, "no-such-event"}})
		Expect(status).To(Equal(http.StatusNoContent), string(raw))
		read := map[string]bool{}
		for _, it := range activityOf(key(k)) {
			read[it.ID] = it.Read
		}
		Expect(read).To(HaveKeyWithValue(arrived.ID, true))
		for _, it := range activityOf(key(other)) {
			if it.ID == arrived.ID {
				Expect(it.Read).To(BeFalse(), "read state is per user")
			}
		}
	})

	It("a file that goes with nothing in its place is a removal, named", func() {
		film := stack.Seeded.Movies[0]
		extra, extraID := addFilmFile(film.Path, film.FileID, "activity-removed.mkv")
		startScan(map[string]string{"folder_id": "movies"})
		waitScanned()
		before := activityIDs(activityOf(key(k)))

		_, err := stack.Core.Exec(`DELETE FROM media_files WHERE id = ?`, extraID)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Remove(extra)).To(Succeed())
		startScan(map[string]string{"folder_id": "movies"})
		waitScanned()

		var removed *activityItem
		for _, it := range activityOf(key(k)) {
			if !before[it.ID] && it.Kind == "removed" {
				removed = &it
				break
			}
		}
		Expect(removed).NotTo(BeNil(), scanDump())
		Expect(removed.Title).NotTo(BeNil())
		Expect(removed.Title.ID).To(Equal(film.OwnerID))
		Expect(removed.Name).NotTo(BeEmpty())
	})

	It("merges the episodes of a season that lands together", func() {
		extExec(`DELETE FROM seen WHERE episode_id IS NOT NULL`)
		startScan(map[string]string{"folder_id": "series"})
		waitScanned()
		for _, it := range activityOf(key(k)) {
			if it.Kind == "episodes" {
				Expect(
					it.Episodes,
				).To(HaveLen(len(stack.Seeded.Episodes)), scanDump())
				return
			}
		}
		Fail("no episodes event")
	})

	It("is newest first and at most 100", func() {
		items := activityOf(key(k))
		Expect(len(items)).To(BeNumerically("<=", 100))
		for i := 1; i < len(items); i++ {
			Expect(items[i-1].At >= items[i].At).To(BeTrue())
		}
	})

	It("an empty read list is 400", func() {
		for _, body := range []any{
			map[string]any{"ids": []string{}},
			map[string]any{},
			map[string]any{"ids": "a"},
		} {
			status, raw := call(http.MethodPost, "/activity/read", key(k), body)
			Expect(status).To(Equal(http.StatusBadRequest), string(raw))
			Expect(errorCode(raw)).To(Equal("invalid_request"))
		}
	})
})
