package main

import (
	"encoding/json"

	g "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func jfItem(raw string) jellyfinItem {
	g.GinkgoHelper()
	var it jellyfinItem
	Expect(json.Unmarshal([]byte(raw), &it)).To(Succeed())
	return it
}

var _ = g.Describe("a Jellyfin item as watch state", g.Label("unit"), func() {
	g.It("marks a played movie watched, by its TMDB id", func() {
		it := jfItem(`{"Id": "a1", "Type": "Movie", "RunTimeTicks": 72000000000,
			"ProviderIds": {"Imdb": "tt0133093", "Tmdb": "603"},
			"UserData": {"Played": true, "PlaybackPositionTicks": 0,
				"LastPlayedDate": "2023-11-14T22:13:20.1234567Z"}}`)
		Expect(jellyfinSeen(it)).To(BeTrue())
		w, ok := jellyfinWatch("ana@example.net", it, 0)
		Expect(ok).To(BeTrue())
		Expect(w).To(Equal(item{
			Email: "ana@example.net", TmdbID: 603, Watched: true,
			At: "2023-11-14T22:13:20Z",
		}))
	})

	g.It("keeps the resume position of an episode in progress", func() {
		it := jfItem(`{"Id": "e1", "Type": "Episode", "SeriesId": "s1",
			"ParentIndexNumber": 2, "IndexNumber": 5,
			"RunTimeTicks": 27000000000,
			"UserData": {"Played": false, "PlaybackPositionTicks": 6150000000,
				"LastPlayedDate": "2023-11-14T22:13:20Z"}}`)
		w, ok := jellyfinWatch("ana@example.net", it, 73739)
		Expect(ok).To(BeTrue())
		Expect(w.TvdbID).To(BeEquivalentTo(73739))
		Expect(w.Season).To(BeEquivalentTo(2))
		Expect(w.Episode).To(BeEquivalentTo(5))
		Expect(w.Watched).To(BeFalse())
		Expect(w.Position).To(BeEquivalentTo(615))
		Expect(w.Duration).To(BeEquivalentTo(2700))
	})

	g.It("drops what was never played or has no id", func() {
		Expect(jellyfinSeen(jfItem(`{"Type": "Movie"}`))).To(BeFalse())
		_, ok := jellyfinWatch("ana@example.net",
			jfItem(`{"Type": "Movie", "UserData": {"Played": true}}`), 0)
		Expect(ok).To(BeFalse())
		_, ok = jellyfinWatch("ana@example.net", jfItem(
			`{"Type": "Episode", "SeriesId": "s1", "UserData": {"Played": true}}`,
		), 0)
		Expect(ok).To(BeFalse())
		Expect(providerID(
			jfItem(`{"ProviderIds": {"tvdb": "73739"}}`), "Tvdb",
		)).To(BeEquivalentTo(73739))
	})
})
