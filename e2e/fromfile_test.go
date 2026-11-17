package e2e

import (
	"encoding/json"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/streamline-hq/streamline-player-ext/e2e/harness"
)

var _ = Describe(
	"settings from Streamline's config file",
	Label("e2e"),
	Ordered,
	func() {
		var filed *harness.Stack

		do := func(method, path string, body any) (int, []byte) {
			GinkgoHelper()
			status, raw, err := filed.Call(method, path, key(filed.AdminKey), body)
			Expect(err).NotTo(HaveOccurred())
			return status, raw
		}

		view := func() settingsView {
			GinkgoHelper()
			status, raw := do(http.MethodGet, "/admin/settings", nil)
			Expect(status).To(Equal(http.StatusOK), string(raw))
			var v settingsView
			Expect(json.Unmarshal(raw, &v)).To(Succeed())
			return v
		}

		refused := func(status int, raw []byte) {
			GinkgoHelper()
			Expect(status).To(Equal(http.StatusConflict), string(raw))
			var e struct {
				Error struct{ Code, Owner string }
			}
			Expect(json.Unmarshal(raw, &e)).To(Succeed())
			Expect(e.Error.Code).To(Equal("read_only"))
			Expect(e.Error.Owner).To(Equal("player"))
		}

		BeforeAll(func() {
			filed = harness.Boot(harness.Options{
				Library:  "testdata/library.json",
				Engine:   true,
				FromFile: true,
				Log:      GinkgoWriter,
			})
			DeferCleanup(filed.Close)
		})

		It(
			"reads them, locked, and reaches the engine with the file's secret",
			func() {
				v := view()
				Expect(v.ReadOnly).To(HaveKeyWithValue("player", true))
				Expect(v.Values).To(HaveKeyWithValue("engine.url", filed.EngineURL))
				Expect(v.Values).To(HaveKey("transcoding.hw_accel"),
					"the engine answered a request signed with the file's secret")
			},
		)

		It("lets the file win over what the database holds", func() {
			db := filed.Ext()
			defer db.Close()
			_, err := db.Exec(
				`INSERT INTO settings(key, value) VALUES
				('engine.url', '"http://127.0.0.1:9"'),
				('player.url', '"https://stored.example"')`)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				db := filed.Ext()
				defer db.Close()
				_, err := db.Exec(
					`DELETE FROM settings WHERE key IN ('engine.url', 'player.url')`)
				Expect(err).NotTo(HaveOccurred())
			})
			v := view()
			Expect(v.Values).To(HaveKeyWithValue("engine.url", filed.EngineURL))
			Expect(v.Values).NotTo(
				HaveKeyWithValue("player.url", "https://stored.example"),
				"a setting the file leaves out is unset",
			)
		})

		It(
			"refuses a change to a player setting, and still takes the engine's",
			func() {
				refused(do(http.MethodPatch, "/admin/settings", map[string]any{
					"player.url": "https://player.example",
				}))
				status, raw := do(
					http.MethodPatch,
					"/admin/settings",
					map[string]any{
						"stream.tonemap": false,
					},
				)
				Expect(status).To(Equal(http.StatusOK), string(raw))
			},
		)

		It("reports the secret as set and refuses to replace it", func() {
			status, raw := do(http.MethodGet, "/admin/engine/secret", nil)
			Expect(status).To(Equal(http.StatusOK), string(raw))
			Expect(raw).To(MatchJSON(`{"set": true}`))
			refused(do(http.MethodPost, "/admin/engine/secret", nil))
		})

		It("does not start with a value it cannot use", func() {
			_, err := harness.TryBoot(harness.Options{
				FromFile: true,
				Settings: map[string]any{"player.origins": "not-a-list"},
			})
			Expect(err).To(MatchError(ContainSubstring("settings: player.origins")))

			_, err = harness.TryBoot(harness.Options{
				FromFile: true,
				Settings: map[string]any{
					"engine.url": "http://elsewhere.example:8097",
				},
			})
			Expect(err).To(MatchError(ContainSubstring("hosts does not list")))

			_, err = harness.TryBoot(harness.Options{
				FromFile: true,
				Settings: map[string]any{"stream.tonemap": true},
			})
			Expect(
				err,
			).To(MatchError(ContainSubstring("belongs in the engine's config")))
		})
	},
)
