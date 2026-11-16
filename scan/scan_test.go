package scan_test

import (
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/streamline-hq/streamline-player-ext/scan"
)

func movie(id, owner int64, path string, size, mtime int64) scan.File {
	return scan.File{
		ID: id, MovieID: owner, Path: path, Size: size, MtimeNs: mtime,
	}
}

func seenOf(f scan.File) scan.Seen {
	return scan.Seen{
		ID: f.ID, MovieID: f.MovieID, EpisodeID: f.EpisodeID, Path: f.Path,
		Size: f.Size, MtimeNs: f.MtimeNs,
	}
}

func kinds(changes []scan.Change) map[int64]scan.Kind {
	out := map[int64]scan.Kind{}
	for _, c := range changes {
		out[c.File.ID] = c.Kind
	}
	return out
}

var _ = Describe("Classify", Label("unit"), func() {
	It("classifies new, replaced and gone", func() {
		same := movie(1, 10, "/m/a.mkv", 100, 5)
		changed := movie(2, 11, "/m/b.mkv", 200, 6)
		fresh := movie(3, 12, "/m/c.mkv", 300, 7)
		missing := movie(5, 14, "/m/e.mkv", 500, 9)
		missing.Missing = true

		seen := []scan.Seen{
			seenOf(same),
			seenOf(movie(2, 11, "/m/b.mkv", 150, 6)),
			seenOf(movie(4, 13, "/m/d.mkv", 400, 8)),
			seenOf(movie(5, 14, "/m/e.mkv", 500, 9)),
		}
		changes := scan.Classify(seen, []scan.File{same, changed, fresh, missing})

		Expect(kinds(changes)).To(Equal(map[int64]scan.Kind{
			2: scan.Replaced, 3: scan.New, 4: scan.Gone, 5: scan.Gone,
		}))
		for _, c := range changes {
			if c.Kind == scan.Replaced {
				Expect(c.Previous).NotTo(BeNil())
				Expect(c.Previous.Size).To(Equal(int64(150)))
			}
		}
	})

	It(
		"treats a new file of an owner whose file went away as a replacement",
		func() {
			old := seenOf(movie(1, 10, "/m/old.mkv", 100, 5))
			changes := scan.Classify(
				[]scan.Seen{old},
				[]scan.File{movie(2, 10, "/m/new.mkv", 120, 6)},
			)
			Expect(kinds(changes)).To(Equal(map[int64]scan.Kind{
				1: scan.Gone, 2: scan.Replaced,
			}))
			for _, c := range changes {
				if c.Kind == scan.Replaced {
					Expect(c.Previous.Path).To(Equal("/m/old.mkv"))
				}
			}
		},
	)

	It("replaces the file an owner lost on an earlier page", func() {
		old := seenOf(movie(1, 10, "/m/old.mkv", 100, 5))
		old.GoneAt = 50
		changes := scan.Classify(
			[]scan.Seen{old},
			[]scan.File{movie(7, 10, "/m/new.mkv", 120, 6)},
		)
		Expect(kinds(changes)).To(Equal(map[int64]scan.Kind{7: scan.Replaced}))
		Expect(changes[0].Previous.ID).To(Equal(int64(1)))
	})

	It("keeps a second version of a title that still has its first as new", func() {
		first := movie(1, 10, "/m/a.mkv", 100, 5)
		second := movie(2, 10, "/m/a-4k.mkv", 900, 6)
		changes := scan.Classify(
			[]scan.Seen{seenOf(first)},
			[]scan.File{first, second},
		)
		Expect(kinds(changes)).To(Equal(map[int64]scan.Kind{2: scan.New}))
	})

	It("answers a file that came back as new", func() {
		f := movie(1, 10, "/m/a.mkv", 100, 5)
		s := seenOf(f)
		s.GoneAt = 99
		Expect(kinds(scan.Classify([]scan.Seen{s}, []scan.File{f}))).
			To(Equal(map[int64]scan.Kind{1: scan.New}))
	})

	It("yields each file exactly once, paging with size 2 over 5 files", func() {
		var all []scan.File
		for i := int64(1); i <= 5; i++ {
			all = append(all, movie(i, 100+i, "/m/f.mkv", i, i))
		}
		var seen []scan.Seen
		var cursor int64
		var queued []int64
		for {
			page := pageAfter(all, cursor, 2)
			if len(page) == 0 {
				break
			}
			for _, c := range scan.Classify(windowOf(seen, page, cursor, all), page) {
				Expect(c.Kind).To(Equal(scan.New))
				queued = append(queued, c.File.ID)
				seen = append(seen, seenOf(c.File))
			}
			cursor = page[len(page)-1].ID
		}
		Expect(queued).To(Equal([]int64{1, 2, 3, 4, 5}))
	})

	It("gives a replaced file new job ids", func() {
		old := scan.JobID("analyze", "/m/a.mkv", 100, 5)
		Expect(scan.JobID("analyze", "/m/a.mkv", 101, 5)).NotTo(Equal(old))
		Expect(scan.JobID("analyze", "/m/a.mkv", 100, 6)).NotTo(Equal(old))
		Expect(scan.JobID("analyze", "/m/b.mkv", 100, 5)).NotTo(Equal(old))
		Expect(scan.JobID("trickplay", "/m/a.mkv", 100, 5)).NotTo(Equal(old))
		Expect(scan.JobID("analyze", "/m/a.mkv", 100, 5)).To(Equal(old))
	})

	It("matches the mock's job_id", func() {
		Expect(scan.JobID(
			"analyze", "/media/movies/a b.mkv", 1234, 1700000000123456789,
		)).To(Equal("5cbe4b57b019e331a72eadbdce945cf2"))
	})
})

func pageAfter(all []scan.File, cursor int64, size int) []scan.File {
	i := slices.IndexFunc(all, func(f scan.File) bool { return f.ID > cursor })
	if i < 0 {
		return nil
	}
	return all[i:min(i+size, len(all))]
}

// windowOf is the seen rows the caller hands Classify for one page: those
// within the page's id window.
func windowOf(
	seen []scan.Seen, page []scan.File, cursor int64, all []scan.File,
) []scan.Seen {
	hi := page[len(page)-1].ID
	if hi == all[len(all)-1].ID {
		hi = 1 << 62
	}
	var out []scan.Seen
	for _, s := range seen {
		if s.ID > cursor && s.ID <= hi {
			out = append(out, s)
		}
	}
	return out
}
