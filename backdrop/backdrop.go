// Package backdrop reads what TMDB and TVDB answer about a title's backdrops
// and picks one. It does no I/O, so it is tested natively.
package backdrop

import (
	"encoding/json"
	"time"
)

const (
	// MaxFailures is how many provider errors in a row a title gets before it
	// is left alone, so a broken entry cannot hold a scan up.
	MaxFailures = 3
	// Recheck is how long a title with no backdrop waits for its next try.
	Recheck = 7 * 24 * time.Hour
	// tmdbSize is enough for a 1080p hero, at 150 to 300 KB a file.
	tmdbSize = "/t/p/w1280"
)

// Image is one candidate. Textless images carry no title, which a hero that
// draws its own needs.
type Image struct {
	URL      string
	Textless bool
	Score    float64
}

// Pick takes a textless image over any other, then the best scored. Among
// equals the provider's order stands.
func Pick(images []Image) (Image, bool) {
	var best Image
	found := false
	for _, img := range images {
		if img.URL == "" {
			continue
		}
		if !found || (img.Textless && !best.Textless) ||
			(img.Textless == best.Textless && img.Score > best.Score) {
			best, found = img, true
		}
	}
	return best, found
}

// GaveUp says whether a title that failed again, after failures earlier ones
// in a row, is left alone until its next recheck.
func GaveUp(failures int64) bool { return failures+1 >= MaxFailures }

// TMDBImages reads /3/movie/{id}/images or /3/tv/{id}/images. imageBase is
// TMDB's image host.
func TMDBImages(raw []byte, imageBase string) ([]Image, error) {
	var body struct {
		Backdrops []struct {
			FilePath string  `json:"file_path"`
			Language *string `json:"iso_639_1"`
			Votes    float64 `json:"vote_average"`
		} `json:"backdrops"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	out := make([]Image, 0, len(body.Backdrops))
	for _, b := range body.Backdrops {
		if b.FilePath == "" {
			continue
		}
		out = append(out, Image{
			URL:      imageBase + tmdbSize + b.FilePath,
			Textless: b.Language == nil || *b.Language == "",
			Score:    b.Votes,
		})
	}
	return out, nil
}

// TMDBFind reads /3/find/{tvdb_id}?external_source=tvdb_id: the TMDB id of
// the series, when TMDB knows it.
func TMDBFind(raw []byte) (id int64, found bool, err error) {
	var body struct {
		TV []struct {
			ID int64 `json:"id"`
		} `json:"tv_results"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return 0, false, err
	}
	if len(body.TV) == 0 || body.TV[0].ID == 0 {
		return 0, false, nil
	}
	return body.TV[0].ID, true, nil
}

// TVDBToken reads the answer of /v4/login.
func TVDBToken(raw []byte) (string, error) {
	var body struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	err := json.Unmarshal(raw, &body)
	return body.Data.Token, err
}

// TVDBImages reads /v4/series/{id}/artworks?type=3 (backgrounds).
func TVDBImages(raw []byte) ([]Image, error) {
	var body struct {
		Data struct {
			Artworks []struct {
				Image    string  `json:"image"`
				Language *string `json:"language"`
				Score    float64 `json:"score"`
			} `json:"artworks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	out := make([]Image, 0, len(body.Data.Artworks))
	for _, a := range body.Data.Artworks {
		out = append(out, Image{
			URL:      a.Image,
			Textless: a.Language == nil || *a.Language == "",
			Score:    a.Score,
		})
	}
	return out, nil
}
