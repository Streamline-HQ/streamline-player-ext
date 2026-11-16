// Package scan decides what changed between the files Streamline lists and the
// files the extension has seen. It does no I/O, so paging and replacement are
// specified without a host.
package scan

import (
	"crypto/md5" //nolint:gosec // the engine's job ids are md5 of this string
	"encoding/hex"
	"fmt"
)

type Kind int

const (
	New Kind = iota + 1
	Replaced
	Gone
)

// File is a row of Streamline's video_files view. MtimeNs comes from the file
// itself, which the view does not carry.
type File struct {
	ID, MovieID, EpisodeID int64
	Path                   string
	Size, MtimeNs          int64
	Missing                bool
}

// Seen is a row of the extension's own record. GoneAt is zero while the file
// is live.
type Seen struct {
	ID, MovieID, EpisodeID int64
	Path                   string
	Size, MtimeNs          int64
	GoneAt                 int64
}

// Change is one file's difference. Previous is the file a replacement
// supersedes; File is the one now in the library (the vanished one for Gone).
type Change struct {
	Kind     Kind
	File     File
	Previous *Seen
}

type owner struct{ movie, episode int64 }

func ownerOf(movie, episode int64) owner { return owner{movie, episode} }

// Classify compares one page of the view with the record. seen holds the
// record's rows for the page's id window: from the id after the previous
// page's last to this page's last, or to the end on the final page. A live row
// the page does not carry, or carries as missing, is gone. A file of an owner
// whose file just went is a replacement of it, and so is a file whose own id
// came back changed.
func Classify(seen []Seen, page []File) []Change {
	byID := make(map[int64]Seen, len(seen))
	for _, s := range seen {
		byID[s.ID] = s
	}
	inPage := make(map[int64]File, len(page))
	for _, f := range page {
		inPage[f.ID] = f
	}

	var changes []Change
	gone := map[owner]*Seen{}
	for _, s := range seen {
		if s.GoneAt == 0 {
			continue
		}
		s := s
		key := ownerOf(s.MovieID, s.EpisodeID)
		if prev := gone[key]; prev == nil || s.GoneAt >= prev.GoneAt {
			gone[key] = &s
		}
	}
	for _, s := range seen {
		if s.GoneAt != 0 {
			continue
		}
		if f, ok := inPage[s.ID]; ok && !f.Missing {
			continue
		}
		s := s
		changes = append(changes, Change{
			Kind: Gone,
			File: File{
				ID: s.ID, MovieID: s.MovieID, EpisodeID: s.EpisodeID,
				Path: s.Path, Size: s.Size, MtimeNs: s.MtimeNs, Missing: true,
			},
		})
		gone[ownerOf(s.MovieID, s.EpisodeID)] = &s
	}

	for _, f := range page {
		if f.Missing {
			continue
		}
		s, known := byID[f.ID]
		switch {
		case known && s.GoneAt == 0:
			if s.Path != f.Path || s.Size != f.Size || s.MtimeNs != f.MtimeNs {
				changes = append(changes, Change{
					Kind: Replaced, File: f, Previous: &s,
				})
			}
		default:
			key := ownerOf(f.MovieID, f.EpisodeID)
			if prev := gone[key]; prev != nil && prev.ID != f.ID {
				delete(gone, key)
				changes = append(changes, Change{
					Kind: Replaced, File: f, Previous: prev,
				})
				continue
			}
			changes = append(changes, Change{Kind: New, File: f})
		}
	}
	return changes
}

// JobID is the engine job id of a job on a file, as the mock derives it: the
// same file gives the same id, and a changed file gives a new one.
func JobID(kind, path string, size, mtimeNs int64) string {
	sum := md5.Sum( //nolint:gosec // see the import
		[]byte(fmt.Sprintf("%s|%s|%d|%d", kind, path, size, mtimeNs)),
	)
	return hex.EncodeToString(sum[:])
}
