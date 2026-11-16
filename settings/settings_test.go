package settings

import (
	"errors"

	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = g.Describe("setting validation", g.Label("unit"), func() {
	g.DescribeTable(
		"accepts",
		func(key string, v any) {
			Expect(Validate(key, v)).To(Succeed())
		},
		g.Entry("an https engine URL", "engine.url", "https://engine.lan:8097"),
		g.Entry(
			"an http engine URL with a path",
			"engine.url",
			"http://10.0.0.2:8097/api",
		),
		g.Entry("a cleared URL", "engine.public_url", ""),
		g.Entry("a player URL", "player.url", "https://player.example.com"),
		g.Entry(
			"origins",
			"player.origins",
			[]any{"https://a.example", "http://127.0.0.1:1430"},
		),
		g.Entry("no origins", "player.origins", []any{}),
		g.Entry(
			"an https app link",
			"player.app_links",
			[]any{"https://app.example/cb"},
		),
		g.Entry(
			"a custom scheme app link",
			"player.app_links",
			[]any{"streamline://auth"},
		),
		g.Entry("CIDRs", "network.home", []any{"192.168.1.0/24", "fd00::/8"}),
		g.Entry("a native string list", "network.home", []string{"10.0.0.0/8"}),
		g.Entry("a hardware mode", "transcoding.hw_accel", "vaapi"),
		g.Entry("a device", "transcoding.hw_device", "/dev/dri/renderD128"),
		g.Entry("tonemap", "stream.tonemap", false),
		g.Entry("a live limit", "stream.max_live", float64(4)),
	)

	g.DescribeTable(
		"refuses",
		func(key string, v any) {
			err := Validate(key, v)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, ErrUnknown)).To(BeFalse())
		},
		g.Entry("a relative engine URL", "engine.url", "engine.lan:8097"),
		g.Entry("an ftp URL", "engine.url", "ftp://engine.lan"),
		g.Entry("a URL with a query", "engine.url", "https://engine.lan/?a=b"),
		g.Entry("a bare question mark", "engine.public_url", "https://engine.lan/?"),
		g.Entry("a URL with a fragment", "player.url", "https://p.example/#x"),
		g.Entry("a URL with credentials", "player.url", "https://u:p@p.example"),
		g.Entry("a URL that is not a string", "player.url", 3),
		g.Entry(
			"an origin with a path",
			"player.origins",
			[]any{"https://a.example/x"},
		),
		g.Entry(
			"an origin with a trailing slash",
			"player.origins",
			[]any{"https://a.example/"},
		),
		g.Entry("an origin without a scheme", "player.origins", []any{"a.example"}),
		g.Entry("a non-http origin", "player.origins", []any{"streamline://auth"}),
		g.Entry(
			"origins that are not a list",
			"player.origins",
			"https://a.example",
		),
		g.Entry("a list holding a number", "player.origins", []any{1}),
		g.Entry("an app link without a scheme", "player.app_links", []any{"app/cb"}),
		g.Entry(
			"an app link with a fragment",
			"player.app_links",
			[]any{"https://a.example/cb#x"},
		),
		g.Entry("a bare address as CIDR", "network.home", []any{"192.168.1.5"}),
		g.Entry("a word as CIDR", "network.home", []any{"nope"}),
		g.Entry("an unknown hardware mode", "transcoding.hw_accel", "magic"),
		g.Entry("an empty device", "transcoding.hw_device", ""),
		g.Entry("tonemap as text", "stream.tonemap", "yes"),
		g.Entry("a live limit of zero", "stream.max_live", float64(0)),
		g.Entry("a fractional live limit", "stream.max_live", 2.5),
		g.Entry("a live limit above eight", "stream.max_live", float64(9)),
	)

	g.It("names a key that is not a setting", func() {
		Expect(Validate("library.watch", true)).To(MatchError(ErrUnknown))
	})

	g.It("knows each key's owner and its name in the engine", func() {
		owner, ok := Owner("stream.tonemap")
		Expect(ok).To(BeTrue())
		Expect(owner).To(Equal(OwnerEngine))
		owner, _ = Owner("network.home")
		Expect(owner).To(Equal(OwnerPlayer))
		Expect(EngineName("stream.max_live")).To(Equal("max_transcodes"))
		Expect(EngineName("player.url")).To(BeEmpty())
		name, ok := FromEngine("max_transcodes")
		Expect(ok).To(BeTrue())
		Expect(name).To(Equal("stream.max_live"))
	})
})
