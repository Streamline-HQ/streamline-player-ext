package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/e2e/harness"
)

const webOrigin = "https://player.e2e.local"

func errorCode(raw []byte) string {
	var body struct{ Error struct{ Code string } }
	Expect(json.Unmarshal(raw, &body)).To(Succeed(), string(raw))
	return body.Error.Code
}

func refresh(token string) (int, []byte) {
	status, raw, err := stack.PostToken(map[string]string{
		"grant_type": "refresh_token", "refresh_token": token,
	})
	Expect(err).NotTo(HaveOccurred())
	return status, raw
}

func bearerStatus(access string) int {
	status, _ := callMe(map[string]string{"Authorization": "Bearer " + access})
	return status
}

func keyStatus(key string) int {
	status, _ := callMe(map[string]string{"X-API-Key": key})
	return status
}

// authorizeCode runs /authorize and returns the code and the challenge's
// verifier.
func authorizeCode(u harness.User, redirect string) (code, verifier string) {
	verifier, challenge := harness.PKCE()
	status, loc, raw, err := stack.Authorize(
		u, harness.AuthorizeQuery(redirect, "st-1", challenge),
	)
	Expect(err).NotTo(HaveOccurred())
	Expect(status).To(Equal(http.StatusFound), string(raw))
	target, err := url.Parse(loc)
	Expect(err).NotTo(HaveOccurred())
	Expect(target.Query().Get("state")).To(Equal("st-1"))
	code = target.Query().Get("code")
	Expect(code).NotTo(BeEmpty())
	return code, verifier
}

var _ = Describe("sign-in", Label("e2e"), func() {
	var user harness.User

	BeforeEach(func() {
		user = stack.NewUser("signin")
		stack.SetSetting("player.origins", []string{webOrigin})
	})

	It("signs a browser in through Streamline and refreshes", func() {
		code, verifier := authorizeCode(user, webOrigin+"/cb")
		status, raw, err := stack.PostToken(map[string]string{
			"grant_type": "authorization_code", "code": code,
			"code_verifier": verifier, "redirect_uri": webOrigin + "/cb",
			"device_name": "Firefox",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var first harness.Tokens
		Expect(json.Unmarshal(raw, &first)).To(Succeed())
		Expect(first.TokenType).To(Equal("Bearer"))
		Expect(first.ExpiresIn).To(BeEquivalentTo(900))
		Expect(first.RefreshExpiresIn).To(BeEquivalentTo(30 * 24 * 3600))
		Expect(first.DeviceID).NotTo(BeEmpty())
		Expect(bearerStatus(first.AccessToken)).To(Equal(http.StatusOK))

		status, raw = refresh(first.RefreshToken)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var second harness.Tokens
		Expect(json.Unmarshal(raw, &second)).To(Succeed())
		Expect(second.AccessToken).NotTo(Equal(first.AccessToken))
		Expect(second.RefreshToken).NotTo(Equal(first.RefreshToken))
		Expect(second.DeviceID).To(Equal(first.DeviceID))
		Expect(bearerStatus(second.AccessToken)).To(Equal(http.StatusOK))
		Expect(bearerStatus(first.AccessToken)).To(Equal(http.StatusOK))
	})

	It("revokes a browser when a rotated refresh token comes back", func() {
		first := stack.SignInBrowser(user, webOrigin)
		status, raw := refresh(first.RefreshToken)
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var second harness.Tokens
		Expect(json.Unmarshal(raw, &second)).To(Succeed())
		Expect(bearerStatus(second.AccessToken)).To(Equal(http.StatusOK))

		status, raw = refresh(first.RefreshToken)
		Expect(status).To(Equal(http.StatusBadRequest))
		Expect(errorCode(raw)).To(Equal("invalid_grant"))
		Expect(bearerStatus(second.AccessToken)).To(Equal(http.StatusUnauthorized))
		Expect(bearerStatus(first.AccessToken)).To(Equal(http.StatusUnauthorized))
		status, _ = refresh(second.RefreshToken)
		Expect(status).To(Equal(http.StatusBadRequest))
	})

	It("refuses an expired or unknown refresh token", func() {
		t := stack.SignInBrowser(user, webOrigin)
		ext := stack.Ext()
		defer ext.Close()
		_, err := ext.Exec(
			`UPDATE refresh_tokens SET expires_at = ? WHERE device_id = ?`,
			time.Now().Add(-time.Minute).Unix(), t.DeviceID,
		)
		Expect(err).NotTo(HaveOccurred())
		status, raw := refresh(t.RefreshToken)
		Expect(status).To(Equal(http.StatusBadRequest))
		Expect(errorCode(raw)).To(Equal("invalid_grant"))
		status, raw = refresh("never-issued")
		Expect(status).To(Equal(http.StatusBadRequest))
		Expect(errorCode(raw)).To(Equal("invalid_grant"))
	})

	It("refuses a redirect_uri off the list before redirecting", func() {
		_, challenge := harness.PKCE()
		for _, redirect := range []string{
			"https://evil.example/cb",
			"http://localhost:4000/cb",
			"",
		} {
			status, loc, raw, err := stack.Authorize(
				user, harness.AuthorizeQuery(redirect, "s", challenge),
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(http.StatusBadRequest), redirect)
			Expect(loc).To(BeEmpty())
			Expect(errorCode(raw)).To(Equal("invalid_request"))
		}
	})

	It(
		"gives an app a device key on a loopback redirect and replaces the key of the same client_id",
		func() {
			first := stack.SignInApp(user, "e2e-app")
			Expect(keyStatus(first)).To(Equal(http.StatusOK))
			other := stack.SignInApp(user, "e2e-other-app")
			second := stack.SignInApp(user, "e2e-app")
			Expect(second).NotTo(Equal(first))
			Expect(keyStatus(second)).To(Equal(http.StatusOK))
			Expect(keyStatus(first)).To(Equal(http.StatusUnauthorized))
			Expect(keyStatus(other)).To(Equal(http.StatusOK))
		},
	)

	It("answers an app with its key and key id", func() {
		code, verifier := authorizeCode(user, harness.LoopbackRedirect)
		status, raw, err := stack.PostToken(map[string]string{
			"grant_type": "authorization_code", "code": code,
			"code_verifier": verifier, "redirect_uri": harness.LoopbackRedirect,
			"client_id": "e2e-shape", "name": "Shape", "platform": "linux",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var k struct {
			APIKey string `json:"api_key"`
			KeyID  int64  `json:"key_id"`
		}
		Expect(json.Unmarshal(raw, &k)).To(Succeed())
		Expect(k.APIKey).NotTo(BeEmpty())
		Expect(k.KeyID).To(BeNumerically(">", 0))
	})

	It("keeps apps and browsers on their own redirects", func() {
		code, verifier := authorizeCode(user, harness.LoopbackRedirect)
		status, raw, err := stack.PostToken(map[string]string{
			"grant_type": "authorization_code", "code": code,
			"code_verifier": verifier, "redirect_uri": harness.LoopbackRedirect,
			"device_name": "Not a browser",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusBadRequest), string(raw))
		Expect(errorCode(raw)).To(Equal("invalid_grant"))

		code, verifier = authorizeCode(user, webOrigin+"/cb")
		status, raw, err = stack.PostToken(map[string]string{
			"grant_type": "authorization_code", "code": code,
			"code_verifier": verifier, "redirect_uri": webOrigin + "/cb",
			"client_id": "e2e-web", "name": "Web", "platform": "web",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusBadRequest), string(raw))
		Expect(errorCode(raw)).To(Equal("invalid_grant"))
	})

	It(
		"refuses plain PKCE, a wrong verifier, a moved redirect and a reused code",
		func() {
			_, challenge := harness.PKCE()
			q := harness.AuthorizeQuery(webOrigin+"/cb", "s", challenge)
			q.Set("code_challenge_method", "plain")
			status, loc, raw, err := stack.Authorize(user, q)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(http.StatusBadRequest))
			Expect(loc).To(BeEmpty())
			Expect(errorCode(raw)).To(Equal("invalid_request"))
			q.Del("code_challenge")
			q.Set("code_challenge_method", "S256")
			status, _, _, err = stack.Authorize(user, q)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(http.StatusBadRequest))

			exchange := func(code, verifier, redirect string) (int, []byte) {
				status, raw, err := stack.PostToken(map[string]string{
					"grant_type": "authorization_code", "code": code,
					"code_verifier": verifier, "redirect_uri": redirect,
					"device_name": "Firefox",
				})
				Expect(err).NotTo(HaveOccurred())
				return status, raw
			}
			code, verifier := authorizeCode(user, webOrigin+"/cb")
			status, raw = exchange(code, "not-the-verifier", webOrigin+"/cb")
			Expect(status).To(Equal(http.StatusBadRequest))
			Expect(errorCode(raw)).To(Equal("invalid_grant"))
			status, _ = exchange(code, verifier, webOrigin+"/cb")
			Expect(
				status,
			).To(Equal(http.StatusBadRequest), "a failed try burns the code")

			code, verifier = authorizeCode(user, webOrigin+"/cb")
			status, _ = exchange(code, verifier, webOrigin+"/other")
			Expect(status).To(Equal(http.StatusBadRequest))

			code, verifier = authorizeCode(user, webOrigin+"/cb")
			status, raw = exchange(code, verifier, webOrigin+"/cb")
			Expect(status).To(Equal(http.StatusOK), string(raw))
			status, raw = exchange(code, verifier, webOrigin+"/cb")
			Expect(status).To(Equal(http.StatusBadRequest))
			Expect(errorCode(raw)).To(Equal("invalid_grant"))
		},
	)

	It("refuses an unknown grant type", func() {
		status, raw, err := stack.PostToken(map[string]string{
			"grant_type": "password", "email": user.Email, "password": user.Password,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusBadRequest))
		Expect(errorCode(raw)).To(Equal("unsupported_grant_type"))
	})

	It("tells the sign-in card where to register", func() {
		status, raw, err := stack.Do(
			http.MethodGet, harness.MountBase+"/auth/methods", "", nil,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var body map[string]any
		Expect(json.Unmarshal(raw, &body)).To(Succeed())
		Expect(body).To(HaveKey("register_url"))
		page, _, err := stack.Do(http.MethodGet, "/register", "", nil)
		Expect(err).NotTo(HaveOccurred())
		if page == http.StatusOK {
			Expect(body["register_url"]).To(Equal(stack.BaseURL + "/register"))
		} else {
			Expect(body["register_url"]).To(BeNil())
		}
	})
})
