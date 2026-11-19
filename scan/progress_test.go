package scan_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/scan"
)

var _ = Describe("Progress", Label("unit"), func() {
	It("is the share of the jobs that are done once all is compared", func() {
		Expect(scan.Progress(4, 4, 3, 4, 0)).To(Equal(0.25))
		Expect(scan.Progress(4, 4, 0, 4, 0)).To(Equal(1.0))
		Expect(scan.Progress(4, 4, 4, 4, 0)).To(Equal(0.0))
		Expect(scan.Progress(0, 0, 2, 4, 0)).To(Equal(0.5))
	})

	It("holds that share back while rows are still compared", func() {
		Expect(scan.Progress(2, 4, 2, 4, 0)).To(Equal(0.25))
		Expect(scan.Progress(0, 4, 0, 4, 0)).To(Equal(0.0))
	})

	It("follows the comparison when no job was queued", func() {
		Expect(scan.Progress(1, 2, 0, 0, 0)).To(Equal(0.5))
	})

	It("counts the part of a waiting job the engine has done", func() {
		Expect(scan.Progress(4, 4, 4, 4, 0.5)).To(Equal(0.125))
	})
})

var _ = Describe("ETASeconds", Label("unit"), func() {
	It("gives the waiting jobs the pace of the last ones to finish", func() {
		Expect(scan.ETASeconds(3, 3, time.Minute, 0, 0)).To(BeEquivalentTo(90))
		Expect(
			scan.ETASeconds(100, 101, 10*time.Minute, 0, 0),
		).To(BeEquivalentTo(600))
	})

	It("counts down after a finish, and holds once a job's worth is gone", func() {
		Expect(
			scan.ETASeconds(3, 3, time.Minute, 10*time.Second, 0),
		).To(BeEquivalentTo(80))
		Expect(
			scan.ETASeconds(3, 3, time.Minute, 30*time.Second, 0),
		).To(BeEquivalentTo(60))
		Expect(
			scan.ETASeconds(3, 3, time.Minute, time.Hour, 0),
		).To(BeEquivalentTo(60))
	})

	It("takes off what the engine has done of the waiting jobs", func() {
		Expect(scan.ETASeconds(3, 3, time.Minute, 0, 0.5)).To(BeEquivalentTo(75))
		Expect(
			scan.ETASeconds(3, 3, time.Minute, 10*time.Second, 1.5),
		).To(BeEquivalentTo(45))
		Expect(scan.ETASeconds(1, 3, time.Minute, 0, 4)).To(BeZero())
	})

	It("is 0 with nothing waiting, or fewer than two finishes to go by", func() {
		Expect(scan.ETASeconds(0, 5, time.Minute, 0, 0)).To(BeZero())
		Expect(scan.ETASeconds(5, 1, time.Minute, 0, 0)).To(BeZero())
		Expect(scan.ETASeconds(5, 5, 0, 0, 0)).To(BeZero())
	})
})
