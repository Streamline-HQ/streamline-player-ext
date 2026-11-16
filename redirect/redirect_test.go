package redirect

import (
	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = g.Describe("classifying a redirect_uri", g.Label("unit"), func() {
	origins := []string{"https://player.example", "http://localhost:1430"}
	links := []string{
		"streamline-player://auth/callback",
		"https://app.example/open",
	}

	g.DescribeTable("accepts",
		func(uri string, want Kind) {
			Expect(Classify(uri, origins, links)).To(Equal(want))
		},
		g.Entry("a listed web origin", "https://player.example/cb", Web),
		g.Entry("a listed origin with a port", "http://localhost:1430/", Web),
		g.Entry("loopback on any port", "http://127.0.0.1:41233/cb", Loopback),
		g.Entry("an app link", "streamline-player://auth/callback", App),
		g.Entry("below an app link", "streamline-player://auth/callback/x", App),
		g.Entry("an https app link", "https://app.example/open/now", App),
	)

	g.DescribeTable(
		"refuses",
		func(uri string) {
			Expect(Classify(uri, origins, links)).To(Equal(Refused))
		},
		g.Entry("an empty uri", ""),
		g.Entry("an unlisted origin", "https://evil.example/cb"),
		g.Entry("a lookalike host", "https://player.example.evil.example/cb"),
		g.Entry("another scheme on a listed host", "http://player.example/cb"),
		g.Entry("loopback without a port", "http://127.0.0.1/cb"),
		g.Entry("loopback over https", "https://127.0.0.1:4000/cb"),
		g.Entry("localhost for loopback", "http://localhost:4000/cb"),
		g.Entry(
			"a sibling of an app link path",
			"streamline-player://auth/callbackx",
		),
		g.Entry("another app host", "streamline-player://other/callback"),
		g.Entry("a fragment", "https://player.example/cb#x"),
		g.Entry("userinfo", "https://user@player.example/cb"),
		g.Entry("a relative uri", "/cb"),
	)
})
