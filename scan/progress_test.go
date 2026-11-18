package scan_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/scan"
)

var _ = Describe("Progress", Label("unit"), func() {
	It("is the share of the jobs that are done once all is compared", func() {
		Expect(scan.Progress(4, 4, 3, 4)).To(Equal(0.25))
		Expect(scan.Progress(4, 4, 0, 4)).To(Equal(1.0))
		Expect(scan.Progress(4, 4, 4, 4)).To(Equal(0.0))
		Expect(scan.Progress(0, 0, 2, 4)).To(Equal(0.5))
	})

	It("holds that share back while rows are still compared", func() {
		Expect(scan.Progress(2, 4, 2, 4)).To(Equal(0.25))
		Expect(scan.Progress(0, 4, 0, 4)).To(Equal(0.0))
	})

	It("follows the comparison when no job was queued", func() {
		Expect(scan.Progress(1, 2, 0, 0)).To(Equal(0.5))
	})
})

var _ = Describe("ETASeconds", Label("unit"), func() {
	It("gives the waiting jobs the pace of the last ones to finish", func() {
		Expect(scan.ETASeconds(3, 2, time.Minute)).To(BeEquivalentTo(90))
		Expect(scan.ETASeconds(100, 100, 10*time.Minute)).To(BeEquivalentTo(600))
		Expect(scan.ETASeconds(1, 3, 10*time.Second)).To(BeEquivalentTo(4))
	})

	It("is 0 with nothing waiting, or nothing finished to go by", func() {
		Expect(scan.ETASeconds(0, 5, time.Minute)).To(BeZero())
		Expect(scan.ETASeconds(5, 0, time.Minute)).To(BeZero())
		Expect(scan.ETASeconds(5, 5, 0)).To(BeZero())
	})
})
