package e2e

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/e2e/harness"
)

type libTitle struct {
	ID            int64    `json:"id"`
	Title         string   `json:"title"`
	OriginalTitle string   `json:"original_title"`
	Year          int64    `json:"year"`
	Overview      string   `json:"overview"`
	Genres        []string `json:"genres"`
	Rating        float64  `json:"rating"`
	AddedAt       string   `json:"added_at"`
	Status        string   `json:"status"`
	Monitored     bool     `json:"monitored"`
	TmdbID        int64    `json:"tmdb_id"`
	Runtime       int64    `json:"runtime"`
	Certification string   `json:"certification"`
	Network       string   `json:"network"`
	TotalSeasons  *int     `json:"total_seasons"`
	Episodes      *struct {
		Total   int64 `json:"total"`
		Watched int64 `json:"watched"`
		Started int64 `json:"started"`
	} `json:"episodes"`
	FileSummary struct {
		FileCount  int64  `json:"file_count"`
		Resolution string `json:"resolution"`
		Codec      string `json:"codec"`
		HDR        string `json:"hdr"`
		Audio      string `json:"audio"`
		Channels   string `json:"channels"`
		Size       int64  `json:"size"`
	} `json:"file_summary"`
	MediaFiles []struct {
		ID string `json:"id"`
	} `json:"media_files"`
	Seasons []struct {
		Number   int64  `json:"number"`
		Name     string `json:"name"`
		Total    int    `json:"total"`
		Episodes []struct {
			ID          int64  `json:"id"`
			Number      int64  `json:"number"`
			Title       string `json:"title"`
			Runtime     int64  `json:"runtime"`
			HasFile     bool   `json:"has_file"`
			MediaFileID string `json:"media_file_id"`
			AirDate     string `json:"air_date"`
		} `json:"episodes"`
	} `json:"seasons"`
	Cast []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Character string `json:"character"`
	} `json:"cast"`
}

type libList struct {
	Items []libTitle `json:"items"`
	Total int        `json:"total"`
}

func getLib(path string, k string, into any) {
	status, raw := call(http.MethodGet, path, key(k), nil)
	ExpectWithOffset(1, status).To(Equal(http.StatusOK), string(raw))
	ExpectWithOffset(1, json.Unmarshal(raw, into)).To(Succeed())
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return info.Size()
}

var _ = Describe("library reads", Label("e2e"), Ordered, func() {
	var (
		k          string
		twoSeasons int64
	)

	BeforeAll(func() {
		k = stack.SignInApp(stack.NewUser("library"), "library-app")

		stamp := time.Now().UTC().String()
		res, err := stack.Core.Exec(
			`INSERT INTO tv_shows(create_time, update_time, title, original_title,
				year, overview, series_status, type, monitored, tvdb_id, network,
				certification, runtime, rating, genres)
			VALUES(?, ?, 'Two Seasons', 'Two Seasons', 2020, '', 'ended',
				'standard', 1, 9001, 'Net', 'TV-14', 45, 7.1, '["Drama"]')`,
			stamp, stamp)
		Expect(err).NotTo(HaveOccurred())
		twoSeasons, _ = res.LastInsertId()
		for n, episodes := range []int{2, 1} {
			res, err := stack.Core.Exec(
				`INSERT INTO seasons(create_time, update_time, number, name,
					monitored, monitor_pinned, tv_show_seasons)
				VALUES(?, ?, ?, ?, 1, 0, ?)`,
				stamp, stamp, n+1, "Season "+strconv.Itoa(n+1), twoSeasons)
			Expect(err).NotTo(HaveOccurred())
			seasonID, _ := res.LastInsertId()
			for e := 1; e <= episodes; e++ {
				_, err := stack.Core.Exec(
					`INSERT INTO episodes(create_time, update_time, number, title,
						overview, air_date, monitored, monitor_pinned,
						absolute_number, grab_failures, tvdb_id, status,
						season_episodes)
					VALUES(?, ?, ?, ?, '', ?, 1, 0, 0, 0, 0, 'wanted', ?)`,
					stamp, stamp, e, "Episode "+strconv.Itoa(e), stamp, seasonID)
				Expect(err).NotTo(HaveOccurred())
			}
		}
		// A show the player lists has a file: one episode gets one.
		_, err = stack.Core.Exec(
			`INSERT INTO media_files(create_time, update_time, path, size, quality,
				format, source, container, duration_seconds, video_codec, width,
				height, audio_codec, audio_channels, bitrate, audio_tracks,
				audio_langs, sub_langs, probed_at, bit_depth, tag_failures,
				episode_media_files)
			SELECT ?, ?, '/library/two-seasons/s01e01.mkv', 1, '360p', 'mkv', 'auto',
				'matroska', 60, 'h264', 640, 360, 'aac', 2, 1, 1, 'eng', 'eng', ?,
				8, 0, min(e.id)
			FROM episodes e JOIN seasons se ON se.id = e.season_episodes
			WHERE se.tv_show_seasons = ?`,
			stamp, stamp, stamp, twoSeasons)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterAll(func() {
		for _, q := range []string{
			`DELETE FROM media_files WHERE episode_media_files IN
				(SELECT e.id FROM episodes e JOIN seasons se
					ON se.id = e.season_episodes WHERE se.tv_show_seasons = ?)`,
			`DELETE FROM episodes WHERE season_episodes IN
				(SELECT id FROM seasons WHERE tv_show_seasons = ?)`,
			`DELETE FROM seasons WHERE tv_show_seasons = ?`,
			`DELETE FROM tv_shows WHERE id = ?`,
		} {
			_, err := stack.Core.Exec(q, twoSeasons)
			Expect(err).NotTo(HaveOccurred())
		}
	})

	It("answers no credential with 401", func() {
		for _, p := range []string{"/movies", "/series", "/movies/1", "/people/1"} {
			status, _ := call(http.MethodGet, p, nil, nil)
			Expect(status).To(Equal(http.StatusUnauthorized), p)
		}
	})

	It("lists the movies and serves one with its files and cast", func() {
		var list libList
		getLib("/movies", k, &list)
		Expect(list.Total).To(Equal(2))
		Expect(list.Items).To(HaveLen(2))
		Expect(list.Items[0].Cast).To(BeEmpty(), "the grid carries no cast")
		Expect(list.Items[0].MediaFiles).To(BeEmpty())

		seeded := stack.Seeded.Movies[0]
		var dune libTitle
		getLib("/movies/"+strconv.FormatInt(seeded.OwnerID, 10), k, &dune)
		Expect(dune.Title).To(Equal("Dune: Part Two"))
		Expect(dune.OriginalTitle).To(Equal("Dune: Part Two"))
		Expect(dune.Year).To(BeEquivalentTo(2024))
		Expect(dune.Overview).To(Equal("Paul Atreides joins the Fremen of Arrakis."))
		Expect(dune.Runtime).To(BeEquivalentTo(166))
		Expect(dune.TmdbID).To(BeEquivalentTo(1))
		Expect(dune.Genres).To(Equal([]string{"Science fiction", "Adventure"}))
		Expect(dune.Rating).To(BeNumerically("~", 8.2))
		Expect(dune.Certification).To(Equal("PG-13"))
		Expect(dune.AddedAt).To(Equal("2026-11-02T00:00:00.000Z"))
		Expect(dune.Monitored).To(BeTrue())
		Expect(dune.MediaFiles).To(HaveLen(1))
		Expect(dune.MediaFiles[0].ID).To(Equal(strconv.FormatInt(seeded.FileID, 10)))
		Expect(dune.FileSummary.FileCount).To(BeEquivalentTo(1))
		Expect(dune.FileSummary.Size).To(Equal(fileSize(seeded.Path)))
		Expect(dune.FileSummary.Resolution).To(Equal("360p"))
		Expect(dune.FileSummary.Codec).To(Equal("h264"))
		Expect(dune.FileSummary.Channels).To(MatchRegexp(`^\d\.\d$`))
		Expect(dune.Cast).To(HaveLen(2))
		Expect(dune.Cast[0].Name).To(Equal("Timothée Chalamet"))
		Expect(dune.Cast[0].Character).To(Equal("Paul Atreides"))
		Expect(dune.Cast[1].Name).To(Equal("Zendaya"))
		Expect(dune.Cast[1].Character).To(Equal("Chani"))
	})

	It("names a file's dynamic range, and none for an SDR file", func() {
		seeded := stack.Seeded.Movies[0]
		var before sql.NullString
		Expect(stack.Core.QueryRow(
			`SELECT dynamic_range FROM media_files WHERE id = ?`, seeded.FileID,
		).Scan(&before)).To(Succeed())
		DeferCleanup(func() {
			_, err := stack.Core.Exec(
				`UPDATE media_files SET dynamic_range = ? WHERE id = ?`,
				before, seeded.FileID)
			Expect(err).NotTo(HaveOccurred())
		})
		for stored, named := range map[string]string{
			"sdr": "", "hdr10": "HDR10", "hlg": "HLG", "dv": "Dolby Vision",
		} {
			_, err := stack.Core.Exec(
				`UPDATE media_files SET dynamic_range = ? WHERE id = ?`,
				stored, seeded.FileID)
			Expect(err).NotTo(HaveOccurred())
			var dune libTitle
			getLib("/movies/"+strconv.FormatInt(seeded.OwnerID, 10), k, &dune)
			Expect(dune.FileSummary.HDR).To(Equal(named), stored)
		}
	})

	It("serves a show with its seasons, and sums the episode files", func() {
		var list libList
		getLib("/series", k, &list)
		Expect(list.Total).To(BeNumerically(">=", 2))
		var severance libTitle
		for _, t := range list.Items {
			Expect(t.Seasons).To(BeEmpty(), "the grid carries no seasons")
			if t.Title == "Severance" {
				severance = t
			}
		}
		Expect(severance.ID).NotTo(BeZero())
		want := int64(0)
		for _, f := range stack.Seeded.Episodes {
			want += fileSize(f.Path)
		}
		Expect(severance.FileSummary.FileCount).To(BeEquivalentTo(2))
		Expect(severance.FileSummary.Size).To(Equal(want))

		getLib("/series/"+strconv.FormatInt(severance.ID, 10), k, &severance)
		Expect(severance.Network).To(Equal("Apple TV+"))
		Expect(severance.Status).To(Equal("continuing"))
		Expect(*severance.TotalSeasons).To(Equal(1))
		Expect(severance.Cast).To(HaveLen(1))
		Expect(severance.Cast[0].Character).To(Equal("Mark Scout"))
		Expect(severance.Seasons).To(HaveLen(1))
		s1 := severance.Seasons[0]
		Expect(s1.Number).To(BeEquivalentTo(1))
		Expect(s1.Total).To(Equal(2), "the episode with no file is left out")
		Expect(s1.Episodes).To(HaveLen(2))
		Expect(s1.Episodes[0].Title).To(Equal("Good News About Hell"))
		Expect(s1.Episodes[0].AirDate).To(Equal("2024-01-01"))
		Expect(s1.Episodes[0].HasFile).To(BeTrue())
		Expect(s1.Episodes[0].MediaFileID).To(
			Equal(strconv.FormatInt(stack.Seeded.Episodes[0].FileID, 10)))
		for _, e := range s1.Episodes {
			Expect(e.HasFile).To(BeTrue(), e.Title)
		}

		var two libTitle
		getLib("/series/"+strconv.FormatInt(twoSeasons, 10), k, &two)
		Expect(*two.TotalSeasons).To(Equal(1), "only the season with a file counts")
		Expect(two.Seasons).To(HaveLen(1))
		Expect(two.Seasons[0].Number).To(BeEquivalentTo(1))
		Expect(two.Seasons[0].Episodes).To(HaveLen(1))
	})

	It("counts the signed-in user's watched and started episodes of a show", func() {
		severance := func(k string) libTitle {
			var list libList
			getLib("/series", k, &list)
			for _, t := range list.Items {
				if t.Title == "Severance" {
					return t
				}
			}
			Fail("Severance is not listed")
			return libTitle{}
		}
		mark := func(watched bool) {
			status, raw := call(
				http.MethodPut,
				"/watchstate/"+strconv.FormatInt(
					stack.Seeded.Episodes[0].FileID,
					10,
				),
				key(k),
				map[string]any{"watched": watched},
			)
			Expect(status).To(Equal(http.StatusOK), string(raw))
		}
		DeferCleanup(mark, false)

		counts := func(k string) [3]int64 {
			e := severance(k).Episodes
			Expect(e).NotTo(BeNil())
			return [3]int64{e.Total, e.Watched, e.Started}
		}
		Expect(counts(k)).To(Equal([3]int64{2, 0, 0}))
		mark(true)
		Expect(counts(k)).To(Equal([3]int64{2, 1, 1}))
		other := stack.SignInApp(stack.NewUser("library-other"), "library-other-app")
		Expect(
			counts(other),
		).To(Equal([3]int64{2, 0, 0}), "another user's marks stay theirs")
	})

	It("leaves out a title Streamline has no file for", func() {
		stamp := time.Now().UTC().String()
		res, err := stack.Core.Exec(
			`INSERT INTO movies(create_time, update_time, title, original_title,
				year, overview, runtime, status, monitored, tmdb_id,
				certification, grab_failures, rating, genres)
			VALUES(?, ?, 'Still Wanted', 'Still Wanted', 2026, '', 100, 'wanted',
				1, 9002, '', 0, 0, '[]')`,
			stamp, stamp)
		Expect(err).NotTo(HaveOccurred())
		wanted, _ := res.LastInsertId()
		DeferCleanup(func() {
			_, err := stack.Core.Exec(`DELETE FROM movies WHERE id = ?`, wanted)
			Expect(err).NotTo(HaveOccurred())
		})

		var list libList
		getLib("/movies", k, &list)
		for _, t := range list.Items {
			Expect(t.Title).NotTo(Equal("Still Wanted"))
		}
		status, _ := call(
			http.MethodGet, "/movies/"+strconv.FormatInt(wanted, 10), key(k), nil,
		)
		Expect(status).To(Equal(http.StatusNotFound))
	})

	It("gives an episode without a runtime the show's", func() {
		var two libTitle
		getLib("/series/"+strconv.FormatInt(twoSeasons, 10), k, &two)
		Expect(two.Runtime).To(BeEquivalentTo(45))
		for _, s := range two.Seasons {
			for _, e := range s.Episodes {
				Expect(e.Runtime).To(BeEquivalentTo(45))
			}
		}
	})

	It("answers an unknown or malformed id with 404", func() {
		for _, p := range []string{
			"/movies/99999", "/series/99999", "/movies/abc", "/people/99999",
			"/people/zendaya",
		} {
			status, raw := call(http.MethodGet, p, key(k), nil)
			Expect(status).To(Equal(http.StatusNotFound), p)
			Expect(errorCode(raw)).To(Equal("not_found"), p)
		}
	})

	It("serves a person with their credits, once per title", func() {
		var dune libTitle
		getLib(
			"/movies/"+strconv.FormatInt(stack.Seeded.Movies[0].OwnerID, 10),
			k,
			&dune,
		)
		zendaya := dune.Cast[1].ID

		var p struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Department string `json:"department"`
			Credits    []struct {
				Kind      string `json:"kind"`
				ID        int64  `json:"id"`
				Title     string `json:"title"`
				Year      int64  `json:"year"`
				Character string `json:"character"`
			} `json:"credits"`
			KnownFor []struct {
				Title string `json:"title"`
				Kind  string `json:"kind"`
				ID    int64  `json:"id"`
			} `json:"known_for"`
		}
		getLib("/people/"+zendaya, k, &p)
		Expect(p.ID).To(Equal(zendaya))
		Expect(p.Name).To(Equal("Zendaya"))
		Expect(p.Department).To(Equal("Acting"))
		Expect(p.Credits).To(HaveLen(2))
		characters := map[string]string{}
		for _, c := range p.Credits {
			Expect(c.Kind).To(Equal("movies"))
			characters[c.Title] = c.Character
		}
		Expect(characters).To(Equal(map[string]string{
			"Dune: Part Two":          "Chani",
			"Furiosa: A Mad Max Saga": "Guest",
		}))
		Expect(p.KnownFor).To(HaveLen(2))
	})

	Describe("posters", func() {
		var art string

		BeforeAll(func() {
			art = filepath.Join(stack.DataDir, "posters")
			Expect(
				os.MkdirAll(filepath.Join(art, "movies", "1"), 0o755),
			).To(Succeed())
			Expect(os.WriteFile(
				filepath.Join(art, "movies", "1", "poster.jpg"),
				[]byte("jpeg-bytes"), 0o644)).To(Succeed())
			Expect(
				os.MkdirAll(filepath.Join(art, "tvshows", "7"), 0o755),
			).To(Succeed())
			Expect(os.WriteFile(
				filepath.Join(art, "tvshows", "7", "poster.jpg"),
				[]byte("show-jpeg"), 0o644)).To(Succeed())
			Expect(os.WriteFile(
				filepath.Join(art, "secret.jpg"), []byte("secret"), 0o644,
			)).To(Succeed())
			Expect(
				os.MkdirAll(filepath.Join(art, "people", "3"), 0o755),
			).To(Succeed())
			Expect(os.WriteFile(
				filepath.Join(art, "people", "3", "poster.jpg"),
				[]byte("face-jpeg"), 0o644)).To(Succeed())
			DeferCleanup(func() {
				Expect(os.RemoveAll(filepath.Join(art, "people", "3"))).To(Succeed())
				Expect(os.RemoveAll(filepath.Join(art, "movies", "1"))).To(Succeed())
				Expect(
					os.RemoveAll(filepath.Join(art, "tvshows", "7")),
				).To(Succeed())
				Expect(os.Remove(filepath.Join(art, "secret.jpg"))).To(Succeed())
			})
		})

		It("needs no credential", func() {
			status, raw, err := stack.Call(
				http.MethodGet, "/posters/movies/1/poster.jpg", nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(http.StatusOK))
			Expect(string(raw)).To(Equal("jpeg-bytes"))
		})

		It("serves a person's photo from the picture core keeps", func() {
			status, raw, err := stack.Call(
				http.MethodGet, "/posters/people/3/photo.jpg", nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(http.StatusOK))
			Expect(string(raw)).To(Equal("face-jpeg"))
		})

		It("serves a series poster from core's tvshows folder", func() {
			status, raw, err := stack.Call(
				http.MethodGet, "/posters/series/7/poster.jpg", nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(http.StatusOK))
			Expect(string(raw)).To(Equal("show-jpeg"))
		})

		It("sets the type and a day's private cache", func() {
			req, err := http.NewRequest(http.MethodGet,
				stack.BaseURL+harness.MountBase+"/posters/movies/1/poster.jpg", nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.Header.Get("Content-Type")).To(Equal("image/jpeg"))
			Expect(resp.Header.Get("Cache-Control")).
				To(Equal("private, max-age=86400"))
		})

		Describe("card size", func() {
			poster := func() string {
				return filepath.Join(art, "movies", "5", "poster.jpg")
			}
			writeJPEG := func(file string, w, h int) {
				GinkgoHelper()
				Expect(os.MkdirAll(filepath.Dir(file), 0o755)).To(Succeed())
				var buf bytes.Buffer
				Expect(jpeg.Encode(
					&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil,
				)).To(Succeed())
				Expect(os.WriteFile(file, buf.Bytes(), 0o644)).To(Succeed())
			}
			size := func() image.Point {
				GinkgoHelper()
				status, raw, err := stack.Call(
					http.MethodGet, "/posters/movies/5/poster-w342.jpg", nil, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(status).To(Equal(http.StatusOK))
				cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
				Expect(err).NotTo(HaveOccurred())
				return image.Pt(cfg.Width, cfg.Height)
			}

			BeforeEach(func() {
				DeferCleanup(func() {
					Expect(
						os.RemoveAll(filepath.Join(art, "movies", "5")),
					).To(Succeed())
				})
			})

			It("shrinks a poster to a card's width", func() {
				writeJPEG(poster(), 780, 1170)
				Expect(size()).To(Equal(image.Pt(342, 513)))
			})

			It("follows a poster core replaced", func() {
				writeJPEG(poster(), 780, 1170)
				Expect(size()).To(Equal(image.Pt(342, 513)))
				writeJPEG(poster(), 780, 780)
				later := time.Now().Add(time.Minute)
				Expect(os.Chtimes(poster(), later, later)).To(Succeed())
				Expect(size()).To(Equal(image.Pt(342, 342)))
			})

			It("serves a poster no wider than a card as it is", func() {
				writeJPEG(poster(), 185, 278)
				Expect(size()).To(Equal(image.Pt(185, 278)))
			})

			It("serves the poster when it cannot be shrunk", func() {
				status, raw, err := stack.Call(
					http.MethodGet, "/posters/movies/1/poster-w342.jpg", nil, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(status).To(Equal(http.StatusOK))
				Expect(string(raw)).To(Equal("jpeg-bytes"))
			})
		})

		It(
			"answers a missing file, a bad kind or extension, and a crafted name with 404",
			func() {
				for _, p := range []string{
					"/posters/movies/1/backdrop.jpg",
					"/posters/movies/2/poster.jpg",
					"/posters/movies/1/poster.gif",
					"/posters/albums/1/poster.jpg",
					"/posters/tvshows/7/poster.jpg",
					"/posters/seasons/7/poster.jpg",
					"/posters/episodes/7/poster.jpg",
					"/posters/movies/1/" + url.PathEscape("../secret.jpg"),
					"/posters/movies/1/..%2fsecret.jpg",
					"/posters/movies/..%2f/secret.jpg",
					"/posters/movies/1/%2e%2e%2fsecret.jpg",
					"/posters/movies/1/..",
				} {
					status, _, err := stack.Call(http.MethodGet, p, nil, nil)
					Expect(err).NotTo(HaveOccurred())
					Expect(status).To(Equal(http.StatusNotFound), p)
				}
			},
		)
	})
})
