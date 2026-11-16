package e2e

import (
	"encoding/json"
	"io"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type settingsView struct {
	Values      map[string]any    `json:"values"`
	Owners      map[string]string `json:"owners"`
	ReadOnly    map[string]bool   `json:"read_only"`
	Pending     []string          `json:"pending"`
	RestartKeys []string          `json:"restart_keys"`
}

func adminSettings() settingsView {
	status, raw := call(
		http.MethodGet, "/admin/settings", key(stack.AdminKey), nil,
	)
	Expect(status).To(Equal(http.StatusOK), string(raw))
	var v settingsView
	Expect(json.Unmarshal(raw, &v)).To(Succeed())
	return v
}

func patchSettings(body map[string]any) (int, []byte) {
	return call(http.MethodPatch, "/admin/settings", key(stack.AdminKey), body)
}

type engineSettings struct {
	Values struct {
		Tonemap       bool `json:"tonemap"`
		MaxTranscodes int  `json:"max_transcodes"`
	} `json:"values"`
}

func engineSettingsDirect() engineSettings {
	req, err := http.NewRequest(http.MethodGet, stack.EngineURL+"/v1/settings", nil)
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Authorization", "Bearer "+stack.EngineSecret)
	resp, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	Expect(resp.StatusCode).To(Equal(http.StatusOK), string(raw))
	var e engineSettings
	Expect(json.Unmarshal(raw, &e)).To(Succeed())
	return e
}

var _ = Describe("settings and the engine secret", Label("e2e"), func() {
	It("shows the engine secret once", func() {
		hdr := key(stack.AdminKey)
		status, raw := call(http.MethodGet, "/admin/engine/secret", hdr, nil)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var before map[string]any
		Expect(json.Unmarshal(raw, &before)).To(Succeed())
		Expect(before).To(HaveKeyWithValue("set", true))
		Expect(before["created_at"]).To(BeAssignableToTypeOf(""))
		Expect(before).NotTo(HaveKey("secret"))
		Expect(string(raw)).NotTo(ContainSubstring(stack.EngineSecret))

		DeferCleanup(func() {
			db := stack.Ext()
			defer db.Close()
			_, err := db.Exec(
				`UPDATE engine_secret SET secret = ?`, stack.EngineSecret,
			)
			Expect(err).NotTo(HaveOccurred())
		})
		status, raw = call(http.MethodPost, "/admin/engine/secret", hdr, nil)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var minted struct{ Secret string }
		Expect(json.Unmarshal(raw, &minted)).To(Succeed())
		Expect(minted.Secret).To(HaveLen(43))
		Expect(minted.Secret).NotTo(Equal(stack.EngineSecret))

		status, raw = call(http.MethodGet, "/admin/engine/secret", hdr, nil)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		Expect(string(raw)).To(ContainSubstring(`"set":true`))
		Expect(string(raw)).NotTo(ContainSubstring(minted.Secret))
	})

	It("describes every setting, and the engine's come from the engine", func() {
		v := adminSettings()
		Expect(v.Owners).To(HaveLen(10))
		Expect(v.Owners).To(HaveKeyWithValue("stream.tonemap", "engine"))
		Expect(v.Owners).To(HaveKeyWithValue("network.home", "player"))
		Expect(
			v.ReadOnly,
		).To(Equal(map[string]bool{"engine": false, "player": false}))
		Expect(v.Values).To(HaveKeyWithValue("engine.url", stack.EngineURL))
		Expect(v.Values).To(HaveKeyWithValue("transcoding.hw_accel", "none"))
		Expect(v.Values).To(HaveKeyWithValue("network.home", BeEmpty()))
		Expect(v.Pending).NotTo(BeNil())
		Expect(v.RestartKeys).To(ContainElement("stream.tonemap"))
	})

	It("saves settings all or nothing", func() {
		status, raw := patchSettings(map[string]any{
			"network.home": []string{"not-a-cidr"},
			"player.url":   "https://player.example.com",
		})
		Expect(status).To(Equal(http.StatusBadRequest), string(raw))
		var body struct{ Error struct{ Code, Key string } }
		Expect(json.Unmarshal(raw, &body)).To(Succeed())
		Expect(body.Error.Code).To(Equal("invalid_setting"))
		Expect(body.Error.Key).To(Equal("network.home"))
		after := adminSettings()
		Expect(after.Values).To(HaveKeyWithValue("player.url", ""))
		Expect(after.Values).To(HaveKeyWithValue("network.home", BeEmpty()))

		status, raw = patchSettings(map[string]any{
			"stream.tonemap": !engineSettingsDirect().Values.Tonemap,
			"nonsense.key":   1,
		})
		Expect(status).To(Equal(http.StatusBadRequest), string(raw))
		Expect(errorCode(raw)).To(Equal("invalid_setting"))
	})

	It("keeps player settings", func() {
		DeferCleanup(func() {
			status, raw := patchSettings(map[string]any{"network.home": []string{}})
			Expect(status).To(Equal(http.StatusOK), string(raw))
		})
		status, raw := patchSettings(map[string]any{
			"network.home": []string{"192.168.1.0/24", "fd00::/8"},
		})
		Expect(status).To(Equal(http.StatusOK), string(raw))
		Expect(adminSettings().Values).To(HaveKeyWithValue(
			"network.home", ConsistOf("192.168.1.0/24", "fd00::/8"),
		))
	})

	It("sends the engine's settings to the engine", func() {
		before := engineSettingsDirect()
		DeferCleanup(func() {
			status, raw := patchSettings(map[string]any{
				"stream.tonemap":  before.Values.Tonemap,
				"stream.max_live": before.Values.MaxTranscodes,
			})
			Expect(status).To(Equal(http.StatusOK), string(raw))
		})
		status, raw := patchSettings(map[string]any{
			"stream.tonemap":  !before.Values.Tonemap,
			"stream.max_live": 3,
		})
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var v settingsView
		Expect(json.Unmarshal(raw, &v)).To(Succeed())
		Expect(
			v.Values,
		).To(HaveKeyWithValue("stream.tonemap", !before.Values.Tonemap))
		Expect(v.Pending).To(ContainElement("stream.max_live"))

		direct := engineSettingsDirect()
		Expect(direct.Values.Tonemap).To(Equal(!before.Values.Tonemap))
		Expect(direct.Values.MaxTranscodes).To(Equal(3))

		status, raw = patchSettings(map[string]any{"stream.max_live": 99})
		Expect(status).To(Equal(http.StatusBadRequest), string(raw))
		Expect(engineSettingsDirect().Values.MaxTranscodes).To(Equal(3))
	})

	It("refuses everyone but an admin", func() {
		member := stack.NewUser("settingsmember")
		memberKey := stack.SignInApp(member, "settings-member-app")
		stack.SetSetting("player.origins", []string{webOrigin})
		browser := stack.SignInBrowser(member, webOrigin)
		for _, hdr := range []map[string]string{key(memberKey), bearer(browser)} {
			for _, req := range []struct{ method, path string }{
				{http.MethodGet, "/admin/settings"},
				{http.MethodPatch, "/admin/settings"},
				{http.MethodGet, "/admin/engine/secret"},
				{http.MethodPost, "/admin/engine/secret"},
			} {
				status, raw := call(req.method, req.path, hdr, map[string]any{})
				Expect(
					status,
				).To(Equal(http.StatusForbidden), req.path+" "+string(raw))
				Expect(errorCode(raw)).To(Equal("forbidden"))
			}
		}
		status, raw := call(http.MethodGet, "/admin/settings", nil, nil)
		Expect(status).To(Equal(http.StatusUnauthorized), string(raw))
	})
})
