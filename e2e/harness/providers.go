package harness

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

const (
	tmdbKey   = "e2e-tmdb-key"
	tvdbKey   = "e2e-tvdb-key"
	tvdbToken = "e2e-tvdb-token"
)

// ProviderImage is one backdrop a provider lists. An empty Language is a
// textless image; the image itself is ImageBody(Name).
type ProviderImage struct {
	Name     string
	Language string
	Score    float64
}

// ImageBody is what the providers serve as the image of that name.
func ImageBody(name string) string { return "image:" + name }

// Providers stands in for TMDB and TVDB, on one address. A title it was told
// nothing about is a 404, as it is for them.
type Providers struct {
	URL string

	server *httptest.Server
	mu     sync.Mutex
	movies map[int][]ProviderImage
	found  map[int]int
	tv     map[int][]ProviderImage
	tvdb   map[int][]ProviderImage
}

func newProviders() *Providers {
	p := &Providers{
		movies: map[int][]ProviderImage{}, found: map[int]int{},
		tv: map[int][]ProviderImage{}, tvdb: map[int][]ProviderImage{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /3/movie/{id}/images", p.tmdbImages(p.movies))
	mux.HandleFunc("GET /3/tv/{id}/images", p.tmdbImages(p.tv))
	mux.HandleFunc("GET /3/find/{id}", p.find)
	mux.HandleFunc("POST /v4/login", p.login)
	mux.HandleFunc("GET /v4/series/{id}/artworks", p.artworks)
	image := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		//nolint:gosec // served as image/jpeg to the extension, never to a browser
		_, _ = w.Write([]byte(ImageBody(r.PathValue("name"))))
	}
	mux.HandleFunc("GET /t/p/w1280/{name}", image)
	mux.HandleFunc("GET /art/{name}", image)
	p.server = httptest.NewServer(mux)
	p.URL = p.server.URL
	return p
}

// SetMovie is what TMDB lists for a movie, by its TMDB id.
func (p *Providers) SetMovie(tmdbID int, images ...ProviderImage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.movies[tmdbID] = images
}

// SetSeries is the series TMDB finds for a TVDB id, and what it lists for it.
// A tmdbID of 0 makes TMDB find nothing.
func (p *Providers) SetSeries(tvdbID, tmdbID int, images ...ProviderImage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.found, tvdbID)
	if tmdbID != 0 {
		p.found[tvdbID] = tmdbID
		p.tv[tmdbID] = images
	}
}

// SetTVDB is what TVDB lists for a series.
func (p *Providers) SetTVDB(tvdbID int, images ...ProviderImage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tvdb[tvdbID] = images
}

func bearer(r *http.Request, want string) bool {
	return r.Header.Get("Authorization") == "Bearer "+want
}

func answer(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func language(l string) any {
	if l == "" {
		return nil
	}
	return l
}

func (p *Providers) listed(
	from map[int][]ProviderImage, r *http.Request,
) ([]ProviderImage, bool) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	p.mu.Lock()
	defer p.mu.Unlock()
	images, ok := from[id]
	return images, ok
}

func (p *Providers) tmdbImages(from map[int][]ProviderImage) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !bearer(r, tmdbKey) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		images, ok := p.listed(from, r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		backdrops := []map[string]any{}
		for _, img := range images {
			backdrops = append(backdrops, map[string]any{
				"file_path": "/" + img.Name, "iso_639_1": language(img.Language),
				"vote_average": img.Score,
			})
		}
		answer(w, map[string]any{"backdrops": backdrops})
	}
}

func (p *Providers) find(w http.ResponseWriter, r *http.Request) {
	if !bearer(r, tmdbKey) || r.URL.Query().Get("external_source") != "tvdb_id" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	id, _ := strconv.Atoi(r.PathValue("id"))
	p.mu.Lock()
	tmdbID, ok := p.found[id]
	p.mu.Unlock()
	results := []map[string]any{}
	if ok {
		results = append(results, map[string]any{"id": tmdbID})
	}
	answer(w, map[string]any{"tv_results": results})
}

func (p *Providers) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIKey string `json:"apikey"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.APIKey != tvdbKey {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	answer(w, map[string]any{"data": map[string]any{"token": tvdbToken}})
}

func (p *Providers) artworks(w http.ResponseWriter, r *http.Request) {
	if !bearer(r, tvdbToken) || r.URL.Query().Get("type") != "3" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	images, ok := p.listed(p.tvdb, r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	artworks := []map[string]any{}
	for _, img := range images {
		artworks = append(artworks, map[string]any{
			"image": p.URL + "/art/" + img.Name, "language": language(img.Language),
			"score": img.Score,
		})
	}
	answer(w, map[string]any{"data": map[string]any{"artworks": artworks}})
}

func (p *Providers) host() string { return strings.TrimPrefix(p.URL, "http://") }
