package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/e2e/harness"
)

type homeFeed struct {
	Featured []struct {
		Kind     string  `json:"kind"`
		ID       int64   `json:"id"`
		Reason   string  `json:"reason"`
		Season   *int64  `json:"season"`
		Episodes []int64 `json:"episodes"`
	} `json:"featured"`
	Arrivals []struct {
		Date  string `json:"date"`
		Items []struct {
			Kind string `json:"kind"`
			ID   int64  `json:"id"`
			At   string `json:"at"`
		} `json:"items"`
	} `json:"arrivals"`
}

func seedWatch(
	userID uint32,
	mediaID int64,
	position, duration float64,
	watched bool,
	at int64,
) {
	db := stack.Ext()
	defer db.Close()
	_, err := db.Exec(
		`INSERT INTO watch(user_id, media_id, position, duration, watched, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)`,
		userID,
		mediaID,
		position,
		duration,
		watched,
		at,
	)
	Expect(err).NotTo(HaveOccurred())
}

var _ = Describe("home", Label("e2e"), func() {
	It("puts a half-watched film first as continue", func() {
		user := stack.NewUser("homefilm")
		k := stack.SignInApp(user, "home-app")
		movie := firstVideoFile("movie_media_files")
		seedWatch(user.ID, movie, 500, 1000, false, time.Now().UnixMilli())

		var movieID int64
		Expect(stack.Core.QueryRow(
			`SELECT movie_media_files FROM media_files WHERE id = ?`, movie,
		).Scan(&movieID)).To(Succeed())

		var feed homeFeed
		getJSON("/home", key(k), &feed)
		Expect(feed.Featured).NotTo(BeEmpty())
		Expect(feed.Featured[0].Kind).To(Equal("movies"))
		Expect(feed.Featured[0].ID).To(Equal(movieID))
		Expect(feed.Featured[0].Reason).To(Equal("continue"))
		Expect(len(feed.Featured)).To(BeNumerically("<=", 5))
		titles := map[[2]any]bool{}
		for _, f := range feed.Featured {
			title := [2]any{f.Kind, f.ID}
			Expect(titles).NotTo(HaveKey(title))
			titles[title] = true
		}
	})

	It("names a show that gained episodes since it was last watched", func() {
		user := stack.NewUser("homeshow")
		k := stack.SignInApp(user, "home-show-app")
		episode := firstVideoFile("episode_media_files")
		seedWatch(user.ID, episode, 0, 0, true, 1000)

		var feed homeFeed
		getJSON("/home", key(k), &feed)
		Expect(feed.Featured).NotTo(BeEmpty())
		Expect(feed.Featured[0].Kind).To(Equal("series"))
		Expect(feed.Featured[0].Reason).To(Equal("new_episodes"))
		Expect(feed.Featured[0].Season).NotTo(BeNil())
		Expect(feed.Featured[0].Episodes).NotTo(BeEmpty())
	})

	It("gives an empty library empty featured and arrivals", func() {
		lib := filepath.Join(GinkgoT().TempDir(), "empty.json")
		Expect(
			os.WriteFile(
				lib,
				[]byte(`{"movies":[],"series":[],"people":{}}`),
				0o600,
			),
		).To(Succeed())
		empty := harness.Boot(harness.Options{Library: lib, Log: GinkgoWriter})
		DeferCleanup(empty.Close)

		k := empty.SignInApp(empty.Admin, "empty-app")
		status, raw, err := empty.Call(http.MethodGet, "/home", key(k), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusOK), string(raw))
		Expect(string(raw)).To(MatchJSON(`{"featured":[],"arrivals":[]}`))
	})
})
