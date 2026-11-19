package e2e

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/streamline-hq/streamline-player-ext/e2e/harness"
)

var _ = Describe("backdrops", Label("e2e"), Ordered, func() {
	var backed *harness.Stack

	// served is the backdrop the poster route answers with, "" for a 404.
	served := func(kind string, id int) func() string {
		return func() string {
			resp, err := http.Get(fmt.Sprintf(
				"%s%s/posters/%s/%d/backdrop.jpg",
				backed.BaseURL, harness.MountBase, kind, id))
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				return ""
			}
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(resp.Header.Get("Content-Type")).To(Equal("image/jpeg"))
			Expect(resp.Header.Get("Cache-Control")).
				To(Equal("private, no-cache"))
			raw, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			return string(raw)
		}
	}

	shows := func(kind string, id int, name string) {
		GinkgoHelper()
		Eventually(served(kind, id), time.Minute, 250*time.Millisecond).
			Should(Equal(harness.ImageBody(name)))
	}

	// missingAt is when the title was found to have no backdrop, 0 while it
	// has a row that says otherwise and -1 with no row.
	missingAt := func(kind string, id int) func() int64 {
		return func() int64 {
			db := backed.Ext()
			defer db.Close()
			var at sql.NullInt64
			err := db.QueryRow(
				`SELECT missing_at FROM backdrops WHERE kind = ? AND id = ?`,
				kind, id,
			).Scan(&at)
			if err == sql.ErrNoRows {
				return -1
			}
			Expect(err).NotTo(HaveOccurred())
			return at.Int64
		}
	}

	core := func(method, path string, body any) {
		GinkgoHelper()
		status, raw, err := backed.Do(method, path, backed.AdminToken, body)
		Expect(err).NotTo(HaveOccurred())
		Expect(status/100).To(Equal(2), string(raw))
	}

	BeforeAll(func() {
		backed = harness.Boot(harness.Options{
			Library:   "testdata/library.json",
			Engine:    true,
			Backdrops: true,
			Log:       GinkgoWriter,
		})
		DeferCleanup(backed.Close)
	})

	It("fetches every title's backdrop in a scan, a textless one first", func() {
		backed.Providers.SetMovie(1,
			harness.ProviderImage{Name: "dune-en.jpg", Language: "en", Score: 9},
			harness.ProviderImage{Name: "dune-bare.jpg", Score: 3},
		)
		backed.Providers.SetSeries(1, 77,
			harness.ProviderImage{Name: "show.jpg", Score: 5})
		status, raw, err := backed.Call(
			http.MethodPost, "/admin/library/scan", key(backed.AdminKey),
			map[string]string{},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusAccepted), string(raw))

		shows("movies", 1, "dune-bare.jpg")
		shows("series", 1, "show.jpg")
		Eventually(missingAt("movies", 2), time.Minute, 250*time.Millisecond).
			Should(BeNumerically(">", 0), "neither provider knows the title")
		Expect(served("movies", 2)()).To(BeEmpty())
	})

	It("fetches a title's backdrop when Streamline says it changed", func() {
		backed.Providers.SetMovie(2, harness.ProviderImage{Name: "furiosa.jpg"})
		core(http.MethodPatch, "/api/v1/movies/2", map[string]any{
			"monitored": false,
		})
		shows("movies", 2, "furiosa.jpg")

		backed.Providers.SetMovie(1,
			harness.ProviderImage{Name: "dune-new.jpg", Score: 4})
		core(http.MethodPatch, "/api/v1/movies/1", map[string]any{
			"monitored": false,
		})
		shows("movies", 1, "dune-new.jpg")
	})

	It("asks TVDB for a series TMDB cannot find", func() {
		backed.Providers.SetSeries(1, 0)
		backed.Providers.SetTVDB(1,
			harness.ProviderImage{Name: "tvdb-eng.jpg", Language: "eng", Score: 8},
			harness.ProviderImage{Name: "tvdb-bare.jpg", Score: 1},
		)
		core(http.MethodPatch, "/api/v1/series/1", map[string]any{
			"monitored": false,
		})
		shows("series", 1, "tvdb-bare.jpg")
	})

	It("forgets the backdrop of a title that left Streamline", func() {
		file := filepath.Join(
			backed.DataDir, "extensions", harness.ExtensionName, "store",
			"backdrops", "movies", "2.jpg")
		Expect(file).To(BeAnExistingFile())
		core(http.MethodDelete, "/api/v1/movies/2", nil)
		Eventually(missingAt("movies", 2), time.Minute, 250*time.Millisecond).
			Should(BeEquivalentTo(-1))
		Expect(file).NotTo(BeAnExistingFile())
		Expect(served("movies", 2)()).To(BeEmpty())
	})
})
