package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/e2e/harness"
)

// mp4Only is a client that cannot take the mkv as it is, so the engine
// answers with HLS.
var mp4Only = map[string]any{
	"video": []map[string]any{{
		"codec": "h264", "profiles": []string{"high", "main", "baseline"},
		"max_level": 52, "max_width": 1920, "max_height": 1080, "max_fps": 60,
	}},
	"hdr": map[string]any{
		"formats":     []string{},
		"dv_profiles": []int{},
	},
	"audio": map[string]any{
		"codecs":       []string{"aac"},
		"max_channels": 2,
		"passthrough":  []string{},
	},
	"containers":         []string{"mp4"},
	"subtitles":          []string{"webvtt"},
	"hls_keyframe_exact": false,
}

type startedSession struct {
	SessionID string `json:"session_id"`
	URL       string `json:"url"`
	URLPath   string `json:"url_path"`
}

func startSession(k string, mediaID int64) (int, []byte) {
	return call(http.MethodPost, "/playback/sessions", key(k), map[string]any{
		"media_id":     strconv.FormatInt(mediaID, 10),
		"capabilities": mp4Only,
		"position":     0,
	})
}

func mustStart(k string, mediaID int64) startedSession {
	status, raw := startSession(k, mediaID)
	Expect(status).To(Equal(http.StatusOK), string(raw))
	var s startedSession
	Expect(json.Unmarshal(raw, &s)).To(Succeed())
	Expect(s.SessionID).To(HaveLen(32))
	return s
}

func beat(k, sid string, position float64) (int, []byte) {
	return call(
		http.MethodPost, "/playback/sessions/"+sid+"/progress", key(k),
		map[string]any{"position": position, "paused": false},
	)
}

func get(url string) (int, []byte) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	Expect(err).NotTo(HaveOccurred())
	resp, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	return resp.StatusCode, raw
}

func engineSessionIDs() []string {
	req, err := http.NewRequest(http.MethodGet, stack.EngineURL+"/v1/sessions", nil)
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Authorization", "Bearer "+stack.EngineSecret)
	resp, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	var list struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
		} `json:"sessions"`
	}
	Expect(json.NewDecoder(resp.Body).Decode(&list)).To(Succeed())
	ids := make([]string, 0, len(list.Sessions))
	for _, s := range list.Sessions {
		ids = append(ids, s.SessionID)
	}
	return ids
}

func extExec(query string, args ...any) {
	db := stack.Ext()
	defer db.Close()
	_, err := db.Exec(query, args...)
	Expect(err).NotTo(HaveOccurred())
}

func sessionRows(id string) int {
	db := stack.Ext()
	defer db.Close()
	var n int
	ExpectWithOffset(1, db.QueryRow(
		`SELECT count(*) FROM sessions WHERE id = ?`, id,
	).Scan(&n)).To(Succeed())
	return n
}

func watchOf(k string, mediaID int64) (position float64, watched, found bool) {
	var list struct {
		Items []struct {
			MediaID         string  `json:"media_id"`
			PositionSeconds float64 `json:"position_seconds"`
			Watched         bool    `json:"watched"`
		} `json:"items"`
	}
	getJSON("/watchstate", key(k), &list)
	for _, it := range list.Items {
		if it.MediaID == strconv.FormatInt(mediaID, 10) {
			return it.PositionSeconds, it.Watched, true
		}
	}
	return 0, false, false
}

var _ = Describe("playback", Label("e2e"), Ordered, func() {
	var (
		user  harness.User
		k     string
		movie int64
	)

	BeforeAll(func() {
		user = stack.NewUser("playback")
		k = stack.SignInApp(user, "playback-app")
		movie = firstVideoFile("movie_media_files")
	})

	It("plays a seeded film over HLS, and a tampered signature is refused", func() {
		s := mustStart(k, movie)
		DeferCleanup(func() {
			call(http.MethodDelete, "/playback/sessions/"+s.SessionID, key(k), nil)
		})
		Expect(s.URLPath).NotTo(Equal("file"))
		Expect(
			s.URL,
		).To(HavePrefix(stack.EngineURL + "/hls/" + s.SessionID + "/master.m3u8?exp="))

		status, raw := get(s.URL)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		Expect(string(raw)).To(HavePrefix("#EXTM3U"))

		i := strings.LastIndex(s.URL, "sig=") + len("sig=")
		tampered := s.URL[:i] + strings.Repeat("0", len(s.URL)-i)
		status, _ = get(tampered)
		Expect(status).To(Equal(http.StatusForbidden))

		var info struct {
			SessionID string `json:"session_id"`
		}
		getJSON("/playback/sessions/"+s.SessionID, key(k), &info)
		Expect(info.SessionID).To(Equal(s.SessionID))
	})

	It(
		"answers a medium that does not exist, and a session that is not yours",
		func() {
			status, raw := startSession(k, 999999)
			Expect(status).To(Equal(http.StatusNotFound))
			Expect(errorCode(raw)).To(Equal("media_not_found"))

			s := mustStart(k, movie)
			DeferCleanup(func() {
				call(
					http.MethodDelete,
					"/playback/sessions/"+s.SessionID,
					key(k),
					nil,
				)
			})
			other := stack.SignInApp(stack.NewUser("playother"), "playother-app")
			status, _ = call(
				http.MethodGet,
				"/playback/sessions/"+s.SessionID,
				key(other),
				nil,
			)
			Expect(status).To(Equal(http.StatusNotFound))
			status, _ = beat(other, s.SessionID, 1)
			Expect(status).To(Equal(http.StatusNotFound))
			Expect(engineSessionIDs()).To(ContainElement(s.SessionID))
			status, _ = call(
				http.MethodDelete,
				"/playback/sessions/"+s.SessionID,
				key(other),
				nil,
			)
			Expect(status).To(Equal(http.StatusNotFound))
			Expect(engineSessionIDs()).To(ContainElement(s.SessionID))
		},
	)

	It("answers a quality menu without creating a session", func() {
		before := len(engineSessionIDs())
		status, raw := call(
			http.MethodPost,
			"/playback/decisions",
			key(k),
			map[string]any{
				"media_id":     strconv.FormatInt(movie, 10),
				"capabilities": mp4Only,
				"options":      []map[string]any{{}, {"max_bitrate": 500000}},
			},
		)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var d struct {
			Decisions []map[string]any `json:"decisions"`
		}
		Expect(json.Unmarshal(raw, &d)).To(Succeed())
		Expect(d.Decisions).To(HaveLen(2))
		Expect(engineSessionIDs()).To(HaveLen(before))
	})

	It("patches a session with a fresh url", func() {
		s := mustStart(k, movie)
		DeferCleanup(func() {
			call(http.MethodDelete, "/playback/sessions/"+s.SessionID, key(k), nil)
		})
		status, raw := call(
			http.MethodPatch, "/playback/sessions/"+s.SessionID, key(k),
			map[string]any{"max_bitrate": 1000000, "position": 3},
		)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var patched startedSession
		Expect(json.Unmarshal(raw, &patched)).To(Succeed())
		Expect(patched.SessionID).To(Equal(s.SessionID))
		Expect(patched.URL).To(HavePrefix(stack.EngineURL + "/hls/" + s.SessionID))
	})

	It("writes watch state on a heartbeat, and marks 90 % watched", func() {
		s := mustStart(k, movie)
		DeferCleanup(func() {
			call(http.MethodDelete, "/playback/sessions/"+s.SessionID, key(k), nil)
		})
		status, raw := beat(k, s.SessionID, 2)
		Expect(status).To(Equal(http.StatusNoContent), string(raw))
		position, watched, found := watchOf(k, movie)
		Expect(found).To(BeTrue())
		Expect(position).To(BeNumerically("==", 2))
		Expect(watched).To(BeFalse())

		status, _ = beat(k, s.SessionID, 7.4)
		Expect(status).To(Equal(http.StatusNoContent))
		_, watched, _ = watchOf(k, movie)
		Expect(watched).To(BeTrue())

		status, raw = call(
			http.MethodPost, "/playback/sessions/"+s.SessionID+"/progress", key(k),
			map[string]any{"position": "soon"},
		)
		Expect(status).To(Equal(http.StatusBadRequest), string(raw))
	})

	It("lists the caller's device first, then streaming ones, then by use", func() {
		owner := stack.NewUser("devorder")
		here := stack.SignInApp(owner, "devorder-here")
		stack.SignInApp(owner, "devorder-tv")
		ids := map[string]int64{}
		ext := stack.Ext()
		defer ext.Close()
		rows, err := ext.Query(
			`SELECT client_id, id FROM devices WHERE user_id = ?`, owner.ID,
		)
		Expect(err).NotTo(HaveOccurred())
		for rows.Next() {
			var client string
			var id int64
			Expect(rows.Scan(&client, &id)).To(Succeed())
			ids[client] = id
		}
		Expect(rows.Close()).To(Succeed())
		now := time.Now().Unix()
		_, err = ext.Exec(
			`UPDATE devices SET last_seen = ? WHERE id = ?`,
			now-40,
			ids["devorder-here"],
		)
		Expect(err).NotTo(HaveOccurred())
		_, err = ext.Exec(
			`UPDATE devices SET last_seen = ? WHERE id = ?`,
			now-5,
			ids["devorder-tv"],
		)
		Expect(err).NotTo(HaveOccurred())
		_, err = ext.Exec(
			`INSERT INTO sessions(id, user_id, device_id, media_id, path, started_at,
				last_beat) VALUES('0123456789abcdef0123456789abcdef', ?, ?, 1, '', ?, ?)`,
			owner.ID, ids["devorder-tv"], now, now,
		)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(extExec, `DELETE FROM sessions WHERE user_id = ?`, owner.ID)

		var list struct {
			Items []struct {
				ID        string
				Current   bool
				Streaming bool
			}
		}
		getJSON("/devices", key(here), &list)
		Expect(list.Items).To(HaveLen(2))
		Expect(list.Items[0].Current).To(BeTrue())
		Expect(list.Items[1].Streaming).To(BeTrue())
	})

	It("stops a silent session on the next request", func() {
		s := mustStart(k, movie)
		Expect(engineSessionIDs()).To(ContainElement(s.SessionID))
		extExec(`UPDATE sessions SET last_beat = 0 WHERE id = ?`, s.SessionID)

		status, _ := call(http.MethodGet, "/playback/status", key(k), nil)
		Expect(status).To(Equal(http.StatusOK))
		Expect(engineSessionIDs()).NotTo(ContainElement(s.SessionID))
		status, _ = beat(k, s.SessionID, 1)
		Expect(status).To(Equal(http.StatusNotFound))
	})

	It("forgets a silent session even when the restart halted it", func() {
		s := mustStart(k, movie)
		extExec(
			`UPDATE sessions SET last_beat = 0, stopped_code = 'server_restarting',
				stopped_message = '', stopped_by = 'Ada', retry_until = 1 WHERE id = ?`,
			s.SessionID,
		)
		status, _ := call(http.MethodGet, "/playback/status", key(k), nil)
		Expect(status).To(Equal(http.StatusOK))
		Expect(sessionRows(s.SessionID)).To(BeZero())
	})

	It("keeps browsing quick when the engine hangs on a silent session", func() {
		s := mustStart(k, movie)
		extExec(`UPDATE sessions SET last_beat = 0 WHERE id = ?`, s.SessionID)
		thaw := stack.FreezeEngine()
		DeferCleanup(thaw)

		began := time.Now()
		for _, path := range []string{"/movies", "/me"} {
			status, _ := call(http.MethodGet, path, key(k), nil)
			Expect(status).To(Equal(http.StatusOK), path)
		}
		Expect(time.Since(began)).To(BeNumerically("<", 10*time.Second))

		req, err := http.NewRequest(
			http.MethodGet, stack.BaseURL+harness.MountBase+"/playback/status", nil,
		)
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("X-API-Key", k)
		if resp, err := (&http.Client{Timeout: time.Minute}).Do(req); err == nil {
			resp.Body.Close()
		}
		thaw()
		Expect(
			sessionRows(s.SessionID),
		).To(BeZero(), "forgotten before the engine call")
	})

	It("tells the player an admin stopped it, and that the engine restarts", func() {
		stopped := mustStart(k, movie)
		restarting := mustStart(k, movie)
		DeferCleanup(func() {
			for _, id := range []string{stopped.SessionID, restarting.SessionID} {
				call(http.MethodDelete, "/playback/sessions/"+id, key(k), nil)
			}
		})
		extExec(
			`UPDATE sessions SET stopped_code = 'session_stopped',
				stopped_message = 'Bedtime', stopped_by = 'Ada' WHERE id = ?`,
			stopped.SessionID,
		)
		extExec(
			`UPDATE sessions SET stopped_code = 'server_restarting',
				stopped_message = 'Back soon', stopped_by = 'Ada', retry_until = ?
			WHERE id = ?`,
			time.Now().Unix()+10, restarting.SessionID,
		)

		status, raw := beat(k, stopped.SessionID, 1)
		Expect(status).To(Equal(http.StatusGone), string(raw))
		var e struct {
			Error struct {
				Code, Message, By string
			}
		}
		Expect(json.Unmarshal(raw, &e)).To(Succeed())
		Expect(e.Error.Code).To(Equal("session_stopped"))
		Expect(e.Error.Message).To(Equal("Bedtime"))
		Expect(e.Error.By).To(Equal("Ada"))

		status, raw = beat(k, restarting.SessionID, 1)
		Expect(status).To(Equal(http.StatusServiceUnavailable), string(raw))
		var r struct {
			Error struct {
				Code, Message, By string
				RetryAfter        int `json:"retry_after"`
			}
		}
		Expect(json.Unmarshal(raw, &r)).To(Succeed())
		Expect(r.Error.Code).To(Equal("server_restarting"))
		Expect(r.Error.Message).To(Equal("Back soon"))
		Expect(r.Error.By).To(Equal("Ada"))
		Expect(r.Error.RetryAfter).To(BeNumerically("~", 10, 2))
	})

	It("stops a device's sessions when it is revoked", func() {
		u := stack.NewUser("playrevoke")
		dk := stack.SignInApp(u, "playrevoke-app")
		s := mustStart(dk, movie)
		Expect(engineSessionIDs()).To(ContainElement(s.SessionID))
		status, _ := call(http.MethodDelete, "/devices/current", key(dk), nil)
		Expect(status).To(Equal(http.StatusNoContent))
		Expect(engineSessionIDs()).NotTo(ContainElement(s.SessionID))
	})

	It("lists the subtitle with a signed vtt url that fetches", func() {
		type tracks struct {
			Preparing bool `json:"preparing"`
			Container string
			Subtitles []struct {
				Index int               `json:"index"`
				Files map[string]string `json:"files"`
			} `json:"subtitles"`
			Trickplay *struct{ URL string } `json:"trickplay"`
		}
		var t tracks
		Eventually(func() bool {
			t = tracks{}
			getJSON(
				"/playback/media/"+strconv.FormatInt(movie, 10)+"/tracks",
				key(k),
				&t,
			)
			return !t.Preparing
		}, 60*time.Second, time.Second).Should(BeTrue())
		Expect(t.Container).NotTo(BeEmpty())
		Expect(t.Subtitles).To(HaveLen(1))
		vtt := t.Subtitles[0].Files["vtt"]
		Expect(vtt).To(HavePrefix(stack.EngineURL + "/media/"))
		status, raw := get(vtt)
		Expect(status).To(Equal(http.StatusOK))
		Expect(string(raw)).To(HavePrefix("WEBVTT"))

		status, _ = call(
			http.MethodGet,
			"/playback/media/999999/tracks",
			key(k),
			nil,
		)
		Expect(status).To(Equal(http.StatusNotFound))
	})

	It("says where the device is by the home networks", func() {
		var st struct {
			Available bool   `json:"available"`
			Network   string `json:"network"`
		}
		getJSON("/playback/status", key(k), &st)
		Expect(st.Available).To(BeTrue())
		Expect(st.Network).To(Equal("away"))

		stack.SetSetting("network.home", []string{"127.0.0.0/8"})
		DeferCleanup(func() { stack.SetSetting("network.home", []string{}) })
		getJSON("/playback/status", key(k), &st)
		Expect(st.Network).To(Equal("home"))
	})

	Describe("with the engine out of reach", Ordered, func() {
		BeforeAll(func() {
			stack.SetSetting("engine.url", "http://127.0.0.1:1")
			DeferCleanup(func() { stack.SetSetting("engine.url", stack.EngineURL) })
		})

		It("says it is unavailable and refuses a start in the envelope", func() {
			var st struct{ Available bool }
			getJSON("/playback/status", key(k), &st)
			Expect(st.Available).To(BeFalse())

			status, raw := startSession(k, movie)
			Expect(status).To(Equal(http.StatusServiceUnavailable))
			Expect(errorCode(raw)).To(Equal("engine_unavailable"))

			status, _ = call(http.MethodGet, "/movies", key(k), nil)
			Expect(status).To(Equal(http.StatusOK))
		})
	})
})
