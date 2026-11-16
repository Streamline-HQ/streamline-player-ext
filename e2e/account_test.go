package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/e2e/harness"
)

var (
	pngBytes = append(
		[]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'},
		bytes.Repeat([]byte{7}, 64)...)
	gifBytes = []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")
)

func putPicture(headers map[string]string, body []byte) (int, []byte) {
	req, err := http.NewRequest(
		http.MethodPut, stack.BaseURL+harness.MountBase+"/me/picture",
		bytes.NewReader(body),
	)
	Expect(err).NotTo(HaveOccurred())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	return resp.StatusCode, raw
}

func getJSON(path string, headers map[string]string, v any) {
	status, raw := call(http.MethodGet, path, headers, nil)
	Expect(status).To(Equal(http.StatusOK), string(raw))
	Expect(json.Unmarshal(raw, v)).To(Succeed())
}

func firstVideoFile(ownerColumn string) int64 {
	var id int64
	Expect(stack.Core.QueryRow(
		`SELECT id FROM media_files WHERE ` + ownerColumn +
			` IS NOT NULL ORDER BY id LIMIT 1`,
	).Scan(&id)).To(Succeed())
	return id
}

var _ = Describe("account", Label("e2e"), Ordered, func() {
	var (
		user    harness.User
		apiKey  string
		otherID int64
	)

	BeforeAll(func() {
		stack.SetSetting("player.origins", []string{webOrigin})
		user = stack.NewUser("account")
		apiKey = stack.SignInApp(user, "account-app")
	})

	It("names an admin's role the way the apps test it", func() {
		var me struct{ Role string }
		getJSON("/me", key(stack.AdminKey), &me)
		Expect(me.Role).To(Equal("Admin"))
	})

	It("answers Me with the account and the Streamline it runs in", func() {
		var me struct {
			ID              uint32 `json:"id"`
			Name            string `json:"name"`
			Email           string `json:"email"`
			Role            string `json:"role"`
			LibraryLanguage string `json:"library_language"`
			Picture         bool   `json:"picture"`
			Initials        string `json:"initials"`
			StreamlineURL   string `json:"streamline_url"`
		}
		getJSON("/me", key(apiKey), &me)
		Expect(me.ID).To(Equal(user.ID))
		Expect(me.Email).To(Equal(user.Email))
		Expect(me.Name).To(Equal(strings.Split(user.Email, "@")[0]))
		Expect(me.Role).To(Equal("Member"))
		Expect(me.LibraryLanguage).To(Equal("en"))
		Expect(me.Picture).To(BeFalse())
		Expect(me.Initials).To(MatchRegexp(`^[A-Z0-9]{1,2}$`))
		Expect(me.StreamlineURL).To(Equal(stack.BaseURL))
	})

	It("names the web player in Me once an admin sets player.url", func() {
		var me struct {
			PlayerURL *string `json:"player_url"`
		}
		setPlayerURL := func(u string) {
			status, raw := call(
				http.MethodPatch, "/admin/settings", key(stack.AdminKey),
				map[string]any{"player.url": u},
			)
			Expect(status).To(Equal(http.StatusOK), string(raw))
		}
		setPlayerURL("")
		getJSON("/me", key(apiKey), &me)
		Expect(me.PlayerURL).To(BeNil())

		setPlayerURL("https://watch.example.net")
		DeferCleanup(setPlayerURL, "")
		me.PlayerURL = nil
		getJSON("/me", key(apiKey), &me)
		Expect(me.PlayerURL).To(HaveValue(Equal("https://watch.example.net")))
	})

	It("round-trips a picture, and refuses a 3 MiB body and a GIF", func() {
		h := key(apiKey)
		h["Content-Type"] = "image/png"

		status, raw := putPicture(h, pngBytes)
		Expect(status).To(Equal(http.StatusNoContent), string(raw))

		var me struct{ Picture bool }
		getJSON("/me", key(apiKey), &me)
		Expect(me.Picture).To(BeTrue())

		status, raw = call(
			http.MethodGet,
			"/posters/users/me/photo.jpg",
			key(apiKey),
			nil,
		)
		Expect(status).To(Equal(http.StatusOK))
		Expect(raw).To(Equal(pngBytes))

		mid := append(append([]byte{}, pngBytes...), make([]byte, 768<<10)...)
		status, raw = putPicture(h, mid)
		Expect(status).To(Equal(http.StatusRequestEntityTooLarge))
		Expect(errorCode(raw)).To(Equal("too_large"))

		big := append(append([]byte{}, pngBytes...), make([]byte, 3<<20)...)
		status, _ = putPicture(h, big)
		Expect(status).To(Equal(http.StatusRequestEntityTooLarge))

		status, raw = putPicture(h, gifBytes)
		Expect(status).To(Equal(http.StatusUnsupportedMediaType))
		Expect(errorCode(raw)).To(Equal("unsupported_type"))

		status, _ = call(http.MethodDelete, "/me/picture", key(apiKey), nil)
		Expect(status).To(Equal(http.StatusNoContent))
		getJSON("/me", key(apiKey), &me)
		Expect(me.Picture).To(BeFalse())
		status, _ = call(
			http.MethodGet,
			"/posters/users/me/photo.jpg",
			key(apiKey),
			nil,
		)
		Expect(status).To(Equal(http.StatusNotFound))
	})

	It("refuses /posters/users/me/photo.jpg without a credential", func() {
		status, raw := call(http.MethodGet, "/posters/users/me/photo.jpg", nil, nil)
		Expect(status).To(Equal(http.StatusUnauthorized))
		Expect(errorCode(raw)).To(Equal("unauthorized"))
	})

	It("lists the caller's devices and revokes them", func() {
		browser := stack.SignInBrowser(user, webOrigin)
		stranger := stack.SignInApp(stack.NewUser("stranger"), "stranger-app")

		type device struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			Current   bool   `json:"current"`
			Streaming bool   `json:"streaming"`
			ExpiresAt string `json:"expires_at"`
		}
		var list struct{ Items []device }
		getJSON("/devices", key(apiKey), &list)
		Expect(list.Items).To(HaveLen(2))
		kinds := map[string]device{}
		for _, d := range list.Items {
			kinds[d.Kind] = d
			Expect(d.Streaming).To(BeFalse())
		}
		Expect(kinds["app"].Current).To(BeTrue())
		Expect(kinds["browser"].Current).To(BeFalse())
		Expect(kinds["browser"].ExpiresAt).NotTo(BeEmpty())
		Expect(kinds["app"].ExpiresAt).To(BeEmpty())

		getJSON("/devices", bearer(browser), &list)
		for _, d := range list.Items {
			Expect(d.Current).To(Equal(d.Kind == "browser"))
		}

		var theirs struct{ Items []device }
		getJSON("/devices", key(stranger), &theirs)
		otherID, _ = strconv.ParseInt(theirs.Items[0].ID, 10, 64)
		status, raw := call(
			http.MethodDelete,
			"/devices/"+theirs.Items[0].ID,
			key(apiKey),
			nil,
		)
		Expect(status).To(Equal(http.StatusNotFound), string(raw))
		Expect(keyStatus(stranger)).To(Equal(http.StatusOK))

		status, _ = call(
			http.MethodDelete,
			"/devices/"+kinds["browser"].ID,
			key(apiKey),
			nil,
		)
		Expect(status).To(Equal(http.StatusNoContent))
		Expect(bearerStatus(browser.AccessToken)).To(Equal(http.StatusUnauthorized))
		Expect(otherID).NotTo(BeZero())
	})

	It("makes the next call 401 after revoking the current device", func() {
		k := stack.SignInApp(user, "account-throwaway")
		Expect(keyStatus(k)).To(Equal(http.StatusOK))
		status, _ := call(http.MethodDelete, "/devices/current", key(k), nil)
		Expect(status).To(Equal(http.StatusNoContent))
		Expect(keyStatus(k)).To(Equal(http.StatusUnauthorized))
	})

	It("keeps and lists watch state, most recent first", func() {
		movie, episode := firstVideoFile(
			"movie_media_files",
		), firstVideoFile(
			"episode_media_files",
		)
		put := func(id int64, watched bool) (int, []byte) {
			return call(
				http.MethodPut, "/watchstate/"+strconv.FormatInt(id, 10),
				key(apiKey), map[string]bool{"watched": watched},
			)
		}
		status, raw := put(movie, true)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var w struct {
			MediaID string `json:"media_id"`
			MovieID *int64 `json:"movie_id"`
			Watched bool
		}
		Expect(json.Unmarshal(raw, &w)).To(Succeed())
		Expect(w.MovieID).NotTo(BeNil())
		Expect(w.Watched).To(BeTrue())

		status, raw = put(episode, true)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var ep struct {
			SeriesID  *int64 `json:"series_id"`
			Season    *int64
			EpisodeID *int64 `json:"episode_id"`
		}
		Expect(json.Unmarshal(raw, &ep)).To(Succeed())
		Expect(ep.SeriesID).NotTo(BeNil())
		Expect(ep.Season).NotTo(BeNil())
		Expect(ep.EpisodeID).NotTo(BeNil())

		status, _ = put(movie, false)
		Expect(status).To(Equal(http.StatusOK))
		var list struct {
			Items []struct {
				MediaID string `json:"media_id"`
				Watched bool
			}
		}
		getJSON("/watchstate", key(apiKey), &list)
		Expect(list.Items).To(HaveLen(2))
		Expect(list.Items[0].MediaID).To(Equal(strconv.FormatInt(movie, 10)))
		Expect(list.Items[0].Watched).To(BeFalse())

		status, _ = put(999999, true)
		Expect(status).To(Equal(http.StatusNotFound))
		status, raw = call(
			http.MethodPut, "/watchstate/"+strconv.FormatInt(movie, 10),
			key(apiKey), map[string]string{"nope": "x"},
		)
		Expect(status).To(Equal(http.StatusBadRequest))
		Expect(errorCode(raw)).To(Equal("invalid_request"))
	})
})
