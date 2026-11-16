package sign

import (
	"strings"

	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = g.Describe("sign", g.Label("unit"), func() {
	const (
		secret = "test-secret"
		id     = "0123456789abcdef0123456789abcdef"
		exp    = int64(1767225600)
	)

	g.It("matches the engine's HLS vector", func() {
		Expect(Sign(secret, "hls", id, exp)).To(Equal(
			"398897b4462277ed24b8ba6d55289edfaa2e8a8ed79cad3a28502649b6a375ec"))
	})

	g.It("matches the engine's download vector", func() {
		Expect(Sign(secret, "dl", id, exp)).To(Equal(
			"997950fe99c0e0f162c61a580b7b51e2eb1e992c3e9ee924e6a90e5a4807de88"))
	})

	g.It("keeps the scopes apart", func() {
		Expect(
			Sign(secret, "media", id, exp),
		).NotTo(Equal(Sign(secret, "hls", id, exp)))
	})

	g.It("escapes each segment of a media name and keeps the slashes", func() {
		Expect(EscapePath("fonts/Some Font.ttf")).To(Equal("fonts/Some%20Font.ttf"))
		Expect(strings.Contains(EscapePath("a b"), " ")).To(BeFalse())
	})
})
