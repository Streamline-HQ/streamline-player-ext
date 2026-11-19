package main

import (
	"encoding/json"

	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func meta(raw string) plexMeta {
	g.GinkgoHelper()
	var m plexMeta
	Expect(json.Unmarshal([]byte(raw), &m)).To(Succeed())
	return m
}

var _ = g.Describe("a Plex item as watch state", g.Label("unit"), func() {
	g.It("marks a finished movie watched, by its TMDB id", func() {
		m := meta(`{"guid": "plex://movie/5d77", "viewCount": 2,
			"lastViewedAt": 1700000000, "duration": 7200000,
			"Guid": [{"id": "imdb://tt0133093"}, {"id": "tmdb://603"}]}`)
		Expect(plexSeen(m)).To(BeTrue())
		it, ok := plexItem("ana@example.net", m, 0)
		Expect(ok).To(BeTrue())
		Expect(it).To(Equal(item{
			Email: "ana@example.net", TmdbID: 603, Watched: true,
			At: "2023-11-14T22:13:20Z",
		}))
	})

	g.It("keeps the resume position of an episode in progress", func() {
		m := meta(`{"grandparentRatingKey": "42", "parentIndex": 2, "index": 5,
			"viewCount": 1, "viewOffset": 615000, "duration": 2700000,
			"lastViewedAt": 1700000000}`)
		it, ok := plexItem("ana@example.net", m, 73739)
		Expect(ok).To(BeTrue())
		Expect(it.TvdbID).To(BeEquivalentTo(73739))
		Expect(it.Season).To(BeEquivalentTo(2))
		Expect(it.Episode).To(BeEquivalentTo(5))
		Expect(it.Watched).To(BeFalse())
		Expect(it.Position).To(BeEquivalentTo(615))
		Expect(it.Duration).To(BeEquivalentTo(2700))
	})

	g.It("reads the id of a title matched by a legacy agent", func() {
		show := meta(`{"guid": "com.plexapp.agents.thetvdb://73739?lang=en"}`)
		Expect(externalID(show, "tvdb")).To(BeEquivalentTo(73739))
		Expect(externalID(show, "tmdb")).To(BeZero())
	})

	g.It("drops what was never played or has no id", func() {
		Expect(plexSeen(meta(`{"duration": 7200000}`))).To(BeFalse())
		_, ok := plexItem("ana@example.net", meta(`{"viewCount": 1}`), 0)
		Expect(ok).To(BeFalse())
		_, ok = plexItem("ana@example.net",
			meta(`{"grandparentRatingKey": "42", "viewCount": 1}`), 0)
		Expect(ok).To(BeFalse())
	})
})
