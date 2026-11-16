package route

import (
	"net/http"

	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = g.Describe("the route table", g.Label("unit"), func() {
	var t Table[string]

	g.BeforeEach(func() {
		t = Table[string]{}
		t.Add(http.MethodGet, "/a/{id}/b", "ab")
		t.Add(http.MethodGet, "/a/me", "me")
		t.Add(http.MethodGet, "/a/{id}", "a")
	})

	g.It("binds a {name} segment", func() {
		h, params, status := t.Match(http.MethodGet, "/a/7/b")
		Expect(status).To(Equal(http.StatusOK))
		Expect(h).To(Equal("ab"))
		Expect(params).To(HaveKeyWithValue("id", "7"))
	})

	g.It("treats a trailing slash as no match", func() {
		_, _, status := t.Match(http.MethodGet, "/a/7/b/")
		Expect(status).To(Equal(http.StatusNotFound))
	})

	g.It("answers 405 for a known path under another method", func() {
		_, _, status := t.Match(http.MethodPost, "/a/7/b")
		Expect(status).To(Equal(http.StatusMethodNotAllowed))
	})

	g.It("answers 404 for an unknown path", func() {
		_, _, status := t.Match(http.MethodGet, "/nope")
		Expect(status).To(Equal(http.StatusNotFound))
	})

	g.It("prefers the pattern with more literal segments", func() {
		h, _, _ := t.Match(http.MethodGet, "/a/me")
		Expect(h).To(Equal("me"))
		h, params, _ := t.Match(http.MethodGet, "/a/you")
		Expect(h).To(Equal("a"))
		Expect(params).To(HaveKeyWithValue("id", "you"))
	})

	g.It("does not let a {name} match an empty segment", func() {
		_, _, status := t.Match(http.MethodGet, "/a//b")
		Expect(status).To(Equal(http.StatusNotFound))
	})
})
