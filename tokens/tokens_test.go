package tokens

import (
	"encoding/base64"
	"strings"

	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = g.Describe("tokens", g.Label("unit"), func() {
	g.It("matches the RFC 7636 appendix B pair and nothing else", func() {
		const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		const challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
		Expect(PKCEMatches(verifier, challenge)).To(BeTrue())
		Expect(PKCEMatches(verifier+"x", challenge)).To(BeFalse())
		Expect(PKCEMatches(verifier, "")).To(BeFalse())
	})

	g.It("never accepts the plain method", func() {
		Expect(PKCEMatches("plain", "plain")).To(BeFalse())
		const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		Expect(PKCEMatches(verifier, verifier)).To(BeFalse())
	})

	g.It("makes 32 random bytes of unpadded base64url", func() {
		a, b := New(), New()
		Expect(a).NotTo(Equal(b))
		Expect(a).To(HaveLen(43))
		Expect(a).NotTo(ContainSubstring("="))
		raw, err := base64.RawURLEncoding.DecodeString(a)
		Expect(err).NotTo(HaveOccurred())
		Expect(raw).To(HaveLen(32))
	})

	g.It("hashes to the SHA-256 hex of the secret", func() {
		Expect(Hash("abc")).To(Equal(
			"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		))
	})

	g.It("signs as the engine does", func() {
		Expect(
			Sign("test-secret", "hls|0123456789abcdef0123456789abcdef|1767225600"),
		).
			To(Equal(
				"398897b4462277ed24b8ba6d55289edfaa2e8a8ed79cad3a28502649b6a375ec",
			))
		Expect(
			Sign("test-secret", "dl|0123456789abcdef0123456789abcdef|1767225600"),
		).
			To(Equal(
				"997950fe99c0e0f162c61a580b7b51e2eb1e992c3e9ee924e6a90e5a4807de88",
			))
	})

	g.It("makes user codes of six letters from the alphabet", func() {
		for range 200 {
			code := UserCode()
			Expect(code).To(HaveLen(6))
			Expect(strings.Trim(code, "BCDFGHJKLMNPQRSTVWXZ")).To(BeEmpty())
		}
	})

	g.It("normalizes user codes", func() {
		code, ok := NormalizeUserCode("bcd-fgh")
		Expect(ok).To(BeTrue())
		Expect(code).To(Equal("BCDFGH"))
		code, ok = NormalizeUserCode("BCDFGH")
		Expect(ok).To(BeTrue())
		Expect(code).To(Equal("BCDFGH"))
		for _, bad := range []string{
			"BCDFG", "BCDFGHJ", "BCD--FGH", "BCDFGA", "", "bcd fgh", "-BCDFGH-",
		} {
			_, ok := NormalizeUserCode(bad)
			Expect(ok).To(BeFalse(), bad)
		}
	})
})
