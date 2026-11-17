package scan_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/scan"
)

var _ = Describe("Progress", Label("unit"), func() {
	It("weighs the comparison and the jobs alike", func() {
		Expect(scan.Progress(1, 4, 3, 4)).To(Equal(0.25))
		Expect(scan.Progress(4, 4, 0, 4)).To(Equal(1.0))
		Expect(scan.Progress(0, 4, 4, 4)).To(Equal(0.0))
	})

	It("counts a half with nothing in it as done", func() {
		Expect(scan.Progress(0, 0, 2, 4)).To(Equal(0.75))
		Expect(scan.Progress(1, 2, 0, 0)).To(Equal(0.75))
	})
})

var _ = Describe("ETASeconds", Label("unit"), func() {
	It("extrapolates the rate so far and rounds up", func() {
		Expect(scan.ETASeconds(time.Minute, 0.5)).To(BeEquivalentTo(60))
		Expect(scan.ETASeconds(time.Minute, 0.75)).To(BeEquivalentTo(20))
		Expect(scan.ETASeconds(10*time.Second, 0.3)).To(BeEquivalentTo(24))
	})

	It("has nothing to say without measurable progress or once done", func() {
		Expect(scan.ETASeconds(time.Minute, 0)).To(BeZero())
		Expect(scan.ETASeconds(time.Minute, scan.Measurable/2)).To(BeZero())
		Expect(scan.ETASeconds(time.Minute, 1)).To(BeZero())
		Expect(scan.ETASeconds(0, 0.5)).To(BeZero())
	})
})
