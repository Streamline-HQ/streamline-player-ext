package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/e2e/harness"
)

func hashOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// callMe sends GET /me to the extension with the given headers.
func callMe(headers map[string]string) (int, []byte) {
	req, err := http.NewRequest(
		http.MethodGet, stack.BaseURL+harness.MountBase+"/me", nil,
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

var _ = Describe("credentials", Label("e2e"), func() {
	var (
		user   harness.User
		key    string
		devID  int64
		bearer string
	)

	BeforeEach(func() {
		user = stack.NewUser("cred")
		key = "device-key-" + user.Email
		bearer = "access-token-" + user.Email

		ext := stack.Ext()
		defer ext.Close()
		res, err := ext.Exec(
			`INSERT INTO devices(user_id, kind, client_id, name, platform, key_hash,
				created_at) VALUES(?, 'app', 'spec', 'Spec', 'linux', ?, ?)`,
			user.ID, hashOf(key), time.Now().Unix(),
		)
		Expect(err).NotTo(HaveOccurred())
		devID, err = res.LastInsertId()
		Expect(err).NotTo(HaveOccurred())

		res, err = ext.Exec(
			`INSERT INTO devices(user_id, kind, name, created_at)
			VALUES(?, 'browser', 'Spec browser', ?)`,
			user.ID, time.Now().Unix(),
		)
		Expect(err).NotTo(HaveOccurred())
		browser, err := res.LastInsertId()
		Expect(err).NotTo(HaveOccurred())
		_, err = ext.Exec(
			`INSERT INTO access_tokens(hash, device_id, expires_at) VALUES(?, ?, ?)`,
			hashOf(bearer), browser, time.Now().Add(time.Hour).Unix(),
		)
		Expect(err).NotTo(HaveOccurred())
	})

	It("serves a device key and a live access token", func() {
		status, raw := callMe(map[string]string{"X-API-Key": key})
		Expect(status).To(Equal(http.StatusOK), string(raw))
		var me struct{ Name, Email, Role string }
		Expect(json.Unmarshal(raw, &me)).To(Succeed())
		Expect(me.Email).To(Equal(user.Email))
		Expect(me.Role).To(Equal("Member"))

		status, raw = callMe(map[string]string{
			"Authorization": "Bearer " + bearer,
		})
		Expect(status).To(Equal(http.StatusOK), string(raw))
	})

	It(
		"answers 401 in the envelope without a credential or with a wrong one",
		func() {
			for _, headers := range []map[string]string{
				nil,
				{"X-API-Key": "nope"},
				{"Authorization": "Bearer nope"},
				{"Authorization": "Basic Zm9vOmJhcg=="},
			} {
				status, raw := callMe(headers)
				Expect(status).To(Equal(http.StatusUnauthorized), string(raw))
				var body struct{ Error struct{ Code string } }
				Expect(json.Unmarshal(raw, &body)).To(Succeed())
				Expect(body.Error.Code).To(Equal("unauthorized"))
			}
		},
	)

	It("lets the device key win when both are presented", func() {
		status, _ := callMe(map[string]string{
			"X-API-Key":     key,
			"Authorization": "Bearer nope",
		})
		Expect(status).To(Equal(http.StatusOK))
		status, _ = callMe(map[string]string{
			"X-API-Key":     "nope",
			"Authorization": "Bearer " + bearer,
		})
		Expect(status).To(Equal(http.StatusUnauthorized))
	})

	It("refuses an expired access token", func() {
		ext := stack.Ext()
		defer ext.Close()
		_, err := ext.Exec(
			`UPDATE access_tokens SET expires_at = ? WHERE hash = ?`,
			time.Now().Add(-time.Minute).Unix(), hashOf(bearer),
		)
		Expect(err).NotTo(HaveOccurred())
		status, _ := callMe(map[string]string{
			"Authorization": "Bearer " + bearer,
		})
		Expect(status).To(Equal(http.StatusUnauthorized))
	})

	It("refuses the credentials of a locked account", func() {
		_, err := stack.Core.Exec(
			`UPDATE users SET locked_until = ? WHERE id = ?`,
			time.Now().Add(time.Hour).UTC().Format(time.RFC3339), user.ID,
		)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_, err := stack.Core.Exec(
				`UPDATE users SET locked_until = NULL WHERE id = ?`, user.ID,
			)
			Expect(err).NotTo(HaveOccurred())
		})
		status, _ := callMe(map[string]string{"X-API-Key": key})
		Expect(status).To(Equal(http.StatusUnauthorized))
		status, _ = callMe(map[string]string{
			"Authorization": "Bearer " + bearer,
		})
		Expect(status).To(Equal(http.StatusUnauthorized))
	})

	It("records the use of a device once a minute", func() {
		status, _ := callMe(map[string]string{"X-API-Key": key})
		Expect(status).To(Equal(http.StatusOK))

		ext := stack.Ext()
		defer ext.Close()
		var seen int64
		Expect(ext.QueryRow(
			`SELECT last_seen FROM devices WHERE id = ?`, devID,
		).Scan(&seen)).To(Succeed())
		Expect(seen).To(BeNumerically("~", time.Now().Unix(), 5))

		_, err := ext.Exec(
			`UPDATE devices SET last_seen = ? WHERE id = ?`, seen-1000, devID,
		)
		Expect(err).NotTo(HaveOccurred())
		callMe(map[string]string{"X-API-Key": key})
		var again int64
		Expect(ext.QueryRow(
			`SELECT last_seen FROM devices WHERE id = ?`, devID,
		).Scan(&again)).To(Succeed())
		Expect(again).To(BeNumerically(">", seen-1000))
	})
})
