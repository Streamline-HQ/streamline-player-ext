package envelope

import (
	"encoding/json"

	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = g.Describe("an engine failure", g.Label("unit"), func() {
	g.It("moves retry_after and need inside error and keeps the status", func() {
		status, body := FromEngine(
			503,
			[]byte(
				`{"code":"transcode_limit","message":"m","retry_after":5,"need":"n"}`,
			),
		)
		Expect(status).To(Equal(503))
		Expect(string(body)).To(Equal(
			`{"error":{"code":"transcode_limit","message":"m","retry_after":5,"need":"n"}}`,
		))
	})

	g.It("keeps the engine's status and leaves retry_after out when absent", func() {
		status, body := FromEngine(
			404, []byte(`{"code":"file_not_found","message":"gone"}`),
		)
		Expect(status).To(Equal(404))
		Expect(string(body)).To(Equal(
			`{"error":{"code":"file_not_found","message":"gone"}}`,
		))
	})

	g.DescribeTable(
		"says engine_unavailable",
		func(status int, raw string) {
			got, body := FromEngine(status, []byte(raw))
			Expect(got).To(Equal(503))
			var env struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			Expect(json.Unmarshal(body, &env)).To(Succeed())
			Expect(env.Error.Code).To(Equal("engine_unavailable"))
		},
		g.Entry("for a body that is not JSON", 502, "<html>bad gateway</html>"),
		g.Entry("for an empty body", 500, ""),
		g.Entry("for JSON without a code", 500, `{"message":"m"}`),
		g.Entry(
			"for a refused secret",
			401,
			`{"code":"unauthorized","message":"m"}`,
		),
		g.Entry("for no answer at all", 0, ""),
	)
})
