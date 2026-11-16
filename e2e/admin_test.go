package e2e

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type adminStream struct {
	ID    string `json:"id"`
	User  struct{ Name string }
	Title struct {
		Kind string
		ID   int64
	}
	Device  struct{ Name, Kind string }
	Mode    string
	Network string
}

func adminStreams() []adminStream {
	var list struct{ Items []adminStream }
	getJSON("/admin/streams", key(stack.AdminKey), &list)
	return list.Items
}

var _ = Describe("admin", Label("e2e"), Ordered, func() {
	var (
		k     string
		movie int64
	)

	BeforeAll(func() {
		k = stack.SignInApp(stack.NewUser("adminwatch"), "adminwatch-app")
		movie = firstVideoFile("movie_media_files")
	})

	It("is for admins only", func() {
		for _, path := range []string{
			"/admin/overview", "/admin/streams", "/admin/devices",
		} {
			status, _ := call(http.MethodGet, path, key(k), nil)
			Expect(status).To(Equal(http.StatusForbidden), path)
		}
		status, _ := call(http.MethodPost, "/admin/cache/clear", key(k), nil)
		Expect(status).To(Equal(http.StatusForbidden))
	})

	It("names the engine and its CPU mode in the overview", func() {
		var o struct {
			Extension struct{ Version string }
			Engine    struct {
				Reachable bool
				Version   string
				HW        struct {
					Mode  string
					Ready bool
				}
				APIVersion      int `json:"api_version"`
				AnalysisVersion int `json:"analysis_version"`
			}
			Storage struct {
				Cache struct{ Size, Streams int64 }
			}
			Folders struct{ Reachable, Total int }
			URL     string `json:"streamline_url"`
		}
		getJSON("/admin/overview", key(stack.AdminKey), &o)
		Expect(o.Extension.Version).NotTo(BeEmpty())
		Expect(o.Engine.Reachable).To(BeTrue())
		Expect(o.Engine.Version).NotTo(BeEmpty())
		Expect(o.Engine.APIVersion).To(Equal(1))
		Expect(o.Engine.AnalysisVersion).To(BeNumerically(">", 0))
		Expect(o.Engine.HW.Mode).To(Equal("software"))
		Expect(o.Engine.HW.Ready).To(BeTrue())
		Expect(o.Folders.Total).To(Equal(2))
		Expect(o.Folders.Reachable).To(Equal(2))
		Expect(o.URL).To(Equal(stack.BaseURL))
	})

	It("shows only the engine's reachability when it is out of reach", func() {
		stack.SetSetting("engine.url", "http://127.0.0.1:1")
		DeferCleanup(func() { stack.SetSetting("engine.url", stack.EngineURL) })
		var o struct {
			Engine map[string]any
		}
		getJSON("/admin/overview", key(stack.AdminKey), &o)
		Expect(o.Engine).To(Equal(map[string]any{"reachable": false}))
	})

	It("lists a live stream, and stopping it reaches the player", func() {
		s := mustStart(k, movie)
		DeferCleanup(func() {
			call(http.MethodDelete, "/playback/sessions/"+s.SessionID, key(k), nil)
		})
		status, _ := beat(k, s.SessionID, 1)
		Expect(status).To(Equal(http.StatusNoContent))

		var mine *adminStream
		for _, it := range adminStreams() {
			if it.ID == s.SessionID {
				mine = &it
			}
		}
		Expect(mine).NotTo(BeNil())
		Expect(mine.Title.Kind).To(Equal("movies"))
		Expect(mine.Device.Kind).NotTo(BeEmpty())

		status, raw := call(
			http.MethodPost, "/admin/streams/"+s.SessionID+"/stop",
			key(stack.AdminKey), map[string]any{"message": "Bedtime"},
		)
		Expect(status).To(Equal(http.StatusNoContent), string(raw))
		Expect(adminStreams()).NotTo(ContainElement(HaveField("ID", s.SessionID)))

		status, raw = beat(k, s.SessionID, 2)
		Expect(status).To(Equal(http.StatusGone), string(raw))
		var e struct {
			Error struct{ Code, Message, By string }
		}
		Expect(json.Unmarshal(raw, &e)).To(Succeed())
		Expect(e.Error.Code).To(Equal("session_stopped"))
		Expect(e.Error.Message).To(Equal("Bedtime"))
		Expect(e.Error.By).To(Equal("e2e-admin"))
		Expect(engineSessionIDs()).NotTo(ContainElement(s.SessionID))

		status, _ = call(
			http.MethodPost, "/admin/streams/"+s.SessionID+"/stop",
			key(stack.AdminKey), map[string]any{},
		)
		Expect(status).To(Equal(http.StatusNotFound))
		status, raw = call(
			http.MethodPost, "/admin/streams/nope/stop",
			key(stack.AdminKey), map[string]any{},
		)
		Expect(status).To(Equal(http.StatusNotFound), string(raw))
		status, _ = call(
			http.MethodPost, "/admin/streams/"+s.SessionID+"/stop",
			key(stack.AdminKey), map[string]any{"message": 3},
		)
		Expect(status).To(Equal(http.StatusBadRequest))
	})

	It("works out a stream's network from where its device came from", func() {
		stack.SetSetting("network.home", []string{"127.0.0.0/8"})
		DeferCleanup(func() { stack.SetSetting("network.home", []string{}) })
		viewer := stack.SignInApp(stack.NewUser("adminhome"), "adminhome-app")
		s := mustStart(viewer, movie)
		DeferCleanup(func() {
			call(
				http.MethodDelete,
				"/playback/sessions/"+s.SessionID,
				key(viewer),
				nil,
			)
		})
		status, _ := beat(viewer, s.SessionID, 1)
		Expect(status).To(Equal(http.StatusNoContent))
		for _, it := range adminStreams() {
			if it.ID == s.SessionID {
				Expect(it.Network).To(Equal("home"))
				return
			}
		}
		Fail("the stream is not listed")
	})

	It("lists every user's devices with the caller's own first", func() {
		var list struct {
			Items []struct {
				ID      string
				Kind    string
				Current bool
				User    struct{ Name string }
			}
		}
		getJSON("/admin/devices", key(stack.AdminKey), &list)
		Expect(list.Items).NotTo(BeEmpty())
		Expect(list.Items[0].Current).To(BeTrue())
		Expect(list.Items[0].User.Name).To(Equal("e2e-admin"))
		names := make([]string, 0, len(list.Items))
		for _, d := range list.Items {
			names = append(names, d.User.Name)
		}
		Expect(names).To(ContainElement(HavePrefix("adminwatch")))
	})

	It("revokes another's device, and not an admin's own", func() {
		var list struct {
			Items []struct {
				ID      string
				Current bool
				User    struct{ Name string }
			}
		}
		getJSON("/admin/devices", key(stack.AdminKey), &list)
		var own, other string
		for _, d := range list.Items {
			if d.Current {
				own = d.ID
			} else if strings.HasPrefix(d.User.Name, "adminwatch") {
				other = d.ID
			}
		}
		Expect(own).NotTo(BeEmpty())
		Expect(other).NotTo(BeEmpty())

		status, raw := call(
			http.MethodDelete, "/admin/devices/"+own, key(stack.AdminKey), nil,
		)
		Expect(status).To(Equal(http.StatusConflict))
		Expect(errorCode(raw)).To(Equal("current_device"))

		status, _ = call(
			http.MethodDelete, "/admin/devices/"+other, key(stack.AdminKey), nil,
		)
		Expect(status).To(Equal(http.StatusNoContent))
		status, _ = call(http.MethodGet, "/me", key(k), nil)
		Expect(status).To(Equal(http.StatusUnauthorized))
		status, _ = call(
			http.MethodDelete, "/admin/devices/"+other, key(stack.AdminKey), nil,
		)
		Expect(status).To(Equal(http.StatusNotFound))
	})

	It("answers freed bytes when the cache is cleared", func() {
		var out struct{ Freed *int64 }
		status, raw := call(
			http.MethodPost, "/admin/cache/clear", key(stack.AdminKey), nil,
		)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		Expect(json.Unmarshal(raw, &out)).To(Succeed())
		Expect(out.Freed).NotTo(BeNil())
		Expect(*out.Freed).To(BeNumerically(">=", 0))
	})

	It("restarts the engine, and the next beat says so with retry_after 10", func() {
		player := stack.SignInApp(stack.NewUser("adminrestart"), "adminrestart-app")
		s := mustStart(player, movie)

		status, raw := call(
			http.MethodPost, "/admin/restart", key(stack.AdminKey),
			map[string]any{"message": "Back soon"},
		)
		Expect(status).To(Equal(http.StatusAccepted), string(raw))
		DeferCleanup(func() {
			Expect(stack.RestartEngine()).To(Succeed())
			// The 10 s window outlives this spec; a later one in the same stack
			// would start its session into it.
			db := stack.Ext()
			defer db.Close()
			_, err := db.Exec(`DELETE FROM meta WHERE key = 'restart'`)
			Expect(err).NotTo(HaveOccurred())
		})

		status, raw = beat(player, s.SessionID, 1)
		Expect(status).To(Equal(http.StatusServiceUnavailable), string(raw))
		var e struct {
			Error struct {
				Code, Message, By string
				RetryAfter        int `json:"retry_after"`
			}
		}
		Expect(json.Unmarshal(raw, &e)).To(Succeed())
		Expect(e.Error.Code).To(Equal("server_restarting"))
		Expect(e.Error.Message).To(Equal("Back soon"))
		Expect(e.Error.By).To(Equal("e2e-admin"))
		Expect(e.Error.RetryAfter).To(BeNumerically("~", 10, 2))

		newcomer := stack.SignInApp(stack.NewUser("adminlate"), "adminlate-app")
		status, raw = startSession(newcomer, movie)
		Expect(status).To(Equal(http.StatusServiceUnavailable), string(raw))
		Expect(json.Unmarshal(raw, &e)).To(Succeed())
		Expect(e.Error.Code).To(Equal("server_restarting"))
		Expect(e.Error.Message).To(Equal("Back soon"))
		Expect(e.Error.RetryAfter).To(BeNumerically("~", 10, 2))
	})
})
