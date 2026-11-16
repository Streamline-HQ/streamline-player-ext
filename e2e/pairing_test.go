package e2e

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/e2e/harness"
)

type pairing struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func call(
	method, path string,
	headers map[string]string,
	body any,
) (int, []byte) {
	status, raw, err := stack.Call(method, path, headers, body)
	Expect(err).NotTo(HaveOccurred())
	return status, raw
}

func startPairing(clientID string) pairing {
	status, raw := call(http.MethodPost, "/auth/device/code", nil, map[string]string{
		"client_id": clientID, "name": "Living room", "platform": "android-tv",
	})
	Expect(status).To(Equal(http.StatusOK), string(raw))
	var p pairing
	Expect(json.Unmarshal(raw, &p)).To(Succeed())
	return p
}

func pollPairing(deviceCode string) (int, []byte) {
	return call(
		http.MethodPost, "/auth/device/token", nil,
		map[string]string{"device_code": deviceCode},
	)
}

func bearer(t harness.Tokens) map[string]string {
	return map[string]string{"Authorization": "Bearer " + t.AccessToken}
}

func key(k string) map[string]string { return map[string]string{"X-API-Key": k} }

func meEmail(k string) string {
	status, raw := call(http.MethodGet, "/me", key(k), nil)
	Expect(status).To(Equal(http.StatusOK), string(raw))
	var me struct{ Email string }
	Expect(json.Unmarshal(raw, &me)).To(Succeed())
	return me.Email
}

var _ = Describe("pairing", Label("e2e"), Ordered, func() {
	var (
		user     harness.User
		other    harness.User
		browser  harness.Tokens
		adminKey string
	)

	BeforeAll(func() {
		user = stack.NewUser("pair")
		other = stack.NewUser("pairother")
		stack.SetSetting("player.origins", []string{webOrigin})
		browser = stack.SignInBrowser(user, webOrigin)
		adminKey = stack.SignInApp(stack.Admin, "pair-admin-app")
	})

	It("pairs a TV approved from /pair", func() {
		p := startPairing("tv-1")
		Expect(p.UserCode).To(MatchRegexp(`^[A-Z]{3}-[A-Z]{3}$`))
		Expect(p.ExpiresIn).To(Equal(600))
		Expect(p.Interval).To(Equal(5))
		Expect(p.VerificationURI).To(HaveSuffix(harness.MountBase + "/pair"))
		Expect(
			p.VerificationURIComplete,
		).To(Equal(p.VerificationURI + "?code=" + p.UserCode))

		status, raw := pollPairing(p.DeviceCode)
		Expect(status).To(Equal(http.StatusBadRequest))
		Expect(errorCode(raw)).To(Equal("authorization_pending"))
		status, raw = pollPairing(p.DeviceCode)
		Expect(status).To(Equal(http.StatusBadRequest))
		Expect(errorCode(raw)).To(Equal("slow_down"))

		status, raw = call(
			http.MethodGet,
			"/auth/device/"+p.UserCode,
			bearer(browser),
			nil,
		)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var req struct {
			UserCode    string `json:"user_code"`
			Name        string `json:"name"`
			Platform    string `json:"platform"`
			RequestedAt string `json:"requested_at"`
			IP          *string
		}
		Expect(json.Unmarshal(raw, &req)).To(Succeed())
		Expect(req.UserCode).To(Equal(p.UserCode))
		Expect(req.Name).To(Equal("Living room"))
		Expect(req.Platform).To(Equal("android-tv"))
		Expect(req.RequestedAt).NotTo(BeEmpty())
		Expect(req.IP).NotTo(BeNil())
		Expect(*req.IP).To(Equal("127.0.0.1"))

		status, raw = call(
			http.MethodPost,
			"/auth/device/"+p.UserCode+"/approve",
			bearer(browser),
			nil,
		)
		Expect(status).To(Equal(http.StatusNoContent), string(raw))

		status, raw = pollPairing(p.DeviceCode)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var k struct {
			APIKey string `json:"api_key"`
			KeyID  int64  `json:"key_id"`
		}
		Expect(json.Unmarshal(raw, &k)).To(Succeed())
		Expect(k.APIKey).NotTo(BeEmpty())
		Expect(meEmail(k.APIKey)).To(Equal(user.Email))

		status, _ = pollPairing(p.DeviceCode)
		Expect(status).To(Equal(http.StatusBadRequest), "the key is handed out once")
	})

	It("never approves on its own, and refuses a device key on approval", func() {
		p := startPairing("tv-2")
		path := "/auth/device/" + p.UserCode + "/approve"
		status, raw := call(http.MethodPost, path, key(adminKey), nil)
		Expect(status).To(Equal(http.StatusForbidden))
		Expect(errorCode(raw)).To(Equal("session_required"))
		status, raw = call(http.MethodPost, path, nil, nil)
		Expect(status).To(Equal(http.StatusUnauthorized))
		Expect(errorCode(raw)).To(Equal("unauthorized"))
		status, raw = call(
			http.MethodGet,
			"/auth/device/"+p.UserCode,
			key(adminKey),
			nil,
		)
		Expect(status).To(Equal(http.StatusForbidden))
		Expect(errorCode(raw)).To(Equal("session_required"))

		status, raw = pollPairing(p.DeviceCode)
		Expect(errorCode(raw)).To(Equal("authorization_pending"))
		Expect(status).To(Equal(http.StatusBadRequest))
	})

	It("lists users by name without regard to case", func() {
		stack.NewNamedUser("sortlow", "alice sort")
		stack.NewNamedUser("sorthigh", "Bob sort")
		var users struct{ Items []struct{ Name string } }
		getJSON("/admin/users", key(adminKey), &users)
		at := map[string]int{}
		for i, u := range users.Items {
			at[u.Name] = i
		}
		Expect(at).To(HaveKey("alice sort"))
		Expect(at).To(HaveKey("Bob sort"))
		Expect(at["alice sort"]).To(BeNumerically("<", at["Bob sort"]))
	})

	It("lets an admin approve for another user", func() {
		p := startPairing("tv-3")
		status, raw := call(http.MethodGet, "/admin/pairings", key(adminKey), nil)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var list struct {
			Items []struct {
				UserCode  string `json:"user_code"`
				Name      string `json:"name"`
				Platform  string `json:"platform"`
				CreatedAt string `json:"created_at"`
				ExpiresAt string `json:"expires_at"`
			}
		}
		Expect(json.Unmarshal(raw, &list)).To(Succeed())
		codes := make([]string, 0, len(list.Items))
		for _, it := range list.Items {
			codes = append(codes, it.UserCode)
		}
		Expect(codes).To(ContainElement(p.UserCode))

		status, raw = call(http.MethodGet, "/admin/pairings", bearer(browser), nil)
		Expect(status).To(Equal(http.StatusForbidden), string(raw))

		status, raw = call(http.MethodGet, "/admin/users", key(adminKey), nil)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var users struct {
			Items []struct {
				ID   uint32 `json:"id"`
				Name string `json:"name"`
				Role string `json:"role"`
			}
		}
		Expect(json.Unmarshal(raw, &users)).To(Succeed())
		roles := map[string]bool{}
		for _, u := range users.Items {
			roles[u.Role] = true
		}
		Expect(roles).To(HaveKey("Admin"))
		Expect(roles).To(HaveKey("Member"))
		Expect(roles).To(HaveLen(2))
		for _, u := range users.Items {
			Expect(u.Name).NotTo(BeEmpty(), "a nameless account shows its email")
		}
		ids := make([]uint32, 0, len(users.Items))
		for _, u := range users.Items {
			ids = append(ids, u.ID)
		}
		Expect(ids).To(ContainElement(other.ID))

		path := "/admin/pairings/" + p.UserCode + "/approve"
		status, raw = call(
			http.MethodPost,
			path,
			key(adminKey),
			map[string]any{"user_id": 999999},
		)
		Expect(status).To(Equal(http.StatusNotFound))
		Expect(errorCode(raw)).To(Equal("not_found"))
		status, raw = call(http.MethodPost, path, key(adminKey), map[string]any{})
		Expect(status).To(Equal(http.StatusBadRequest))
		Expect(errorCode(raw)).To(Equal("invalid_request"))
		status, _ = call(
			http.MethodPost,
			path,
			bearer(browser),
			map[string]any{"user_id": other.ID},
		)
		Expect(status).To(Equal(http.StatusForbidden))

		status, raw = call(
			http.MethodPost,
			path,
			key(adminKey),
			map[string]any{"user_id": other.ID},
		)
		Expect(status).To(Equal(http.StatusNoContent), string(raw))
		status, raw = pollPairing(p.DeviceCode)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var k struct {
			APIKey string `json:"api_key"`
		}
		Expect(json.Unmarshal(raw, &k)).To(Succeed())
		Expect(meEmail(k.APIKey)).To(Equal(other.Email))
	})

	It("expires and denies", func() {
		expired := startPairing("tv-4")
		ext := stack.Ext()
		defer ext.Close()
		_, err := ext.Exec(
			`UPDATE pairings SET expires_at = 0 WHERE user_code = ?`,
			strings.ReplaceAll(expired.UserCode, "-", ""),
		)
		Expect(err).NotTo(HaveOccurred())
		status, raw := pollPairing(expired.DeviceCode)
		Expect(status).To(Equal(http.StatusBadRequest))
		Expect(errorCode(raw)).To(Equal("expired_token"))
		status, raw = call(
			http.MethodPost,
			"/auth/device/"+expired.UserCode+"/approve",
			bearer(browser),
			nil,
		)
		Expect(status).To(Equal(http.StatusNotFound))
		Expect(errorCode(raw)).To(Equal("no_pending_pairing"))

		denied := startPairing("tv-5")
		status, raw = call(
			http.MethodPost,
			"/auth/device/"+denied.UserCode+"/deny",
			bearer(browser),
			nil,
		)
		Expect(status).To(Equal(http.StatusNoContent), string(raw))
		status, raw = pollPairing(denied.DeviceCode)
		Expect(status).To(Equal(http.StatusBadRequest))
		Expect(errorCode(raw)).To(Equal("access_denied"))
		status, raw = call(
			http.MethodPost,
			"/auth/device/"+denied.UserCode+"/approve",
			bearer(browser),
			nil,
		)
		Expect(status).To(Equal(http.StatusNotFound))
		Expect(errorCode(raw)).To(Equal("no_pending_pairing"))

		byAdmin := startPairing("tv-6")
		status, raw = call(
			http.MethodPost,
			"/admin/pairings/"+byAdmin.UserCode+"/deny",
			key(adminKey),
			nil,
		)
		Expect(status).To(Equal(http.StatusNoContent), string(raw))
		status, raw = pollPairing(byAdmin.DeviceCode)
		Expect(errorCode(raw)).To(Equal("access_denied"))
		Expect(status).To(Equal(http.StatusBadRequest))
		status, raw = call(
			http.MethodPost,
			"/admin/pairings/"+byAdmin.UserCode+"/deny",
			key(adminKey),
			nil,
		)
		Expect(status).To(Equal(http.StatusNotFound))
		Expect(errorCode(raw)).To(Equal("no_pending_pairing"))
	})

	It("accepts the code with or without the dash, any case", func() {
		p := startPairing("tv-7")
		bare := strings.ToLower(strings.ReplaceAll(p.UserCode, "-", ""))
		status, raw := call(
			http.MethodGet,
			"/auth/device/"+bare,
			bearer(browser),
			nil,
		)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		status, raw = call(
			http.MethodPost,
			"/auth/device/"+strings.ToLower(p.UserCode)+"/approve",
			bearer(browser),
			nil,
		)
		Expect(status).To(Equal(http.StatusNoContent), string(raw))
		status, raw = call(
			http.MethodGet,
			"/auth/device/nonsense",
			bearer(browser),
			nil,
		)
		Expect(status).To(Equal(http.StatusNotFound))
		Expect(errorCode(raw)).To(Equal("no_pending_pairing"))
	})

	It("refuses a pairing without a name", func() {
		status, raw := call(
			http.MethodPost,
			"/auth/device/code",
			nil,
			map[string]string{"client_id": "x"},
		)
		Expect(status).To(Equal(http.StatusBadRequest))
		Expect(errorCode(raw)).To(Equal("invalid_request"))
	})
})
