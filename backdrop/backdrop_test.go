package backdrop_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/backdrop"
)

var _ = Describe("Pick", Label("unit"), func() {
	It("takes a textless image over a better scored one with text", func() {
		got, ok := backdrop.Pick([]backdrop.Image{
			{URL: "en-best", Score: 9},
			{URL: "bare-low", Textless: true, Score: 2},
			{URL: "bare-high", Textless: true, Score: 5},
		})
		Expect(ok).To(BeTrue())
		Expect(got.URL).To(Equal("bare-high"))
	})

	It("takes the best scored when every image carries text", func() {
		got, ok := backdrop.Pick([]backdrop.Image{
			{URL: "a", Score: 3}, {URL: "b", Score: 7}, {URL: "c", Score: 7},
		})
		Expect(ok).To(BeTrue())
		Expect(got.URL).To(Equal("b"))
	})

	It("finds nothing among no images, or images with no address", func() {
		_, ok := backdrop.Pick(nil)
		Expect(ok).To(BeFalse())
		_, ok = backdrop.Pick([]backdrop.Image{{Textless: true, Score: 9}})
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("GaveUp", Label("unit"), func() {
	It("leaves a title alone at its third failure in a row", func() {
		Expect(backdrop.GaveUp(0)).To(BeFalse())
		Expect(backdrop.GaveUp(1)).To(BeFalse())
		Expect(backdrop.GaveUp(2)).To(BeTrue())
	})
})

var _ = Describe("provider answers", Label("unit"), func() {
	It("reads TMDB's images, a null language being textless", func() {
		got, err := backdrop.TMDBImages([]byte(`{"backdrops": [
			{"file_path": "/a.jpg", "iso_639_1": null, "vote_average": 5.5},
			{"file_path": "/b.jpg", "iso_639_1": "en", "vote_average": 8},
			{"file_path": ""}
		], "posters": []}`), "https://img.example")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]backdrop.Image{
			{URL: "https://img.example/t/p/w1280/a.jpg", Textless: true, Score: 5.5},
			{URL: "https://img.example/t/p/w1280/b.jpg", Score: 8},
		}))
	})

	It("reads the series TMDB finds for a TVDB id", func() {
		id, found, err := backdrop.TMDBFind(
			[]byte(`{"movie_results": [], "tv_results": [{"id": 1399}]}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(id).To(BeEquivalentTo(1399))

		_, found, err = backdrop.TMDBFind([]byte(`{"tv_results": []}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())

		_, _, err = backdrop.TMDBFind([]byte(`<html>`))
		Expect(err).To(HaveOccurred())
	})

	It("reads TVDB's token and backgrounds", func() {
		token, err := backdrop.TVDBToken([]byte(`{"data": {"token": "t0"}}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(token).To(Equal("t0"))

		got, err := backdrop.TVDBImages([]byte(`{"data": {"artworks": [
			{"image": "https://art.example/1.jpg", "language": "eng", "score": 4},
			{"image": "https://art.example/2.jpg", "language": null, "score": 1}
		]}}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal([]backdrop.Image{
			{URL: "https://art.example/1.jpg", Score: 4},
			{URL: "https://art.example/2.jpg", Textless: true, Score: 1},
		}))
	})
})
