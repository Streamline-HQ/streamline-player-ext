package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/e2e/harness"
)

var _ = Describe("the harness", Label("e2e"), func() {
	It("boots Streamline with the extension granted and the library seeded", func() {
		resp, err := http.Get(stack.BaseURL + harness.MountBase + "/nope")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		raw, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		Expect(json.Unmarshal(raw, &body)).To(Succeed(), string(raw))
		Expect(body.Error.Code).To(Equal("not_found"))

		ext := stack.Ext()
		defer ext.Close()
		var tables int
		Expect(ext.QueryRow(
			`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='meta'`,
		).Scan(&tables)).To(Succeed())
		Expect(tables).To(Equal(1))

		lib, err := os.ReadFile("testdata/library.json")
		Expect(err).NotTo(HaveOccurred())
		var seed struct {
			Movies []json.RawMessage `json:"movies"`
		}
		Expect(json.Unmarshal(lib, &seed)).To(Succeed())
		var movies int
		Expect(stack.Core.QueryRow(`SELECT count(*) FROM movies`).Scan(&movies)).
			To(Succeed())
		Expect(movies).To(Equal(len(seed.Movies)))

		Expect(stack.Seeded.Movies).NotTo(BeEmpty())
		first := stack.Seeded.Movies[0].Path
		Expect(first).To(BeAnExistingFile())
		out, err := exec.Command(
			"ffprobe", "-v", "error", "-show_entries", "format=format_name",
			"-of", "default=nw=1:nk=1", first,
		).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		Expect(strings.TrimSpace(string(out))).To(ContainSubstring("matroska"))
	})
})
