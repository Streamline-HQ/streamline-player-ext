package envelope

import (
	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = g.Describe("the error envelope", g.Label("unit"), func() {
	g.It("holds the code and the message", func() {
		Expect(string(Error("x", "m"))).
			To(Equal(`{"error":{"code":"x","message":"m"}}`))
	})

	g.It("places extra fields inside error, in order", func() {
		Expect(string(Error("x", "m", "key", "k"))).
			To(Equal(`{"error":{"code":"x","message":"m","key":"k"}}`))
		Expect(string(Error("x", "m", "by", "ana", "retry_after", 10))).
			To(Equal(
				`{"error":{"code":"x","message":"m","by":"ana","retry_after":10}}`,
			))
	})

	g.It("escapes what needs it", func() {
		Expect(string(Error("x", `say "hi"`))).
			To(Equal(`{"error":{"code":"x","message":"say \"hi\""}}`))
	})

	g.It("drops a dangling key and a non-string key", func() {
		Expect(string(Error("x", "m", "key"))).
			To(Equal(`{"error":{"code":"x","message":"m"}}`))
		Expect(string(Error("x", "m", 3, "v", "ok", true))).
			To(Equal(`{"error":{"code":"x","message":"m","ok":true}}`))
	})
})
