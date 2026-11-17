package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"strconv"
	"time"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/backdrop"
)

func init() {
	registerSchema(
		// source is the provider's address of the image on disk. A row with
		// neither fetched_at nor missing_at is still to fetch.
		`CREATE TABLE IF NOT EXISTS backdrops(
			kind TEXT NOT NULL,
			id INTEGER NOT NULL,
			source TEXT NOT NULL DEFAULT '',
			fetched_at INTEGER,
			missing_at INTEGER,
			failures INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(kind, id)
		) WITHOUT ROWID`,
	)
}

const (
	tmdbSecretName = "tmdb"
	tvdbSecretName = "tvdb"

	tmdbURLKey      = "backdrops.tmdb_url"
	tmdbImageURLKey = "backdrops.tmdb_image_url"
	tvdbURLKey      = "backdrops.tvdb_url"

	storeFolder   = "store"
	backdropFile  = "backdrop.jpg"
	backdropsPage = 50
	// The providers allow more; this keeps a first scan from looking like a
	// burst to them.
	providerInterval = time.Second / 4
)

// backdropKinds are the contract's kinds that have a backdrop, with the view
// and the provider id each one carries.
var backdropKinds = []struct{ kind, view, providerID string }{
	{"movies", "streamline_movies", "tmdb_id"},
	{"series", "streamline_series", "tvdb_id"},
}

func providerURL(key, fallback string) string {
	if u, ok := trimmedURL(key); ok {
		return u
	}
	return fallback
}

func tmdbURL() string { return providerURL(tmdbURLKey, "https://api.themoviedb.org") }
func tvdbURL() string { return providerURL(tvdbURLKey, "https://api4.thetvdb.com") }
func tmdbImageURL() string {
	return providerURL(tmdbImageURLKey, "https://image.tmdb.org")
}

// backdropStore is the kept folder, when the config file gives it room and a
// TMDB key: without either, backdrops are off.
func backdropStore() (sdk.Folder, bool) {
	if fileSecrets[tmdbSecretName] == "" {
		return sdk.Folder{}, false
	}
	return sdk.Request{Folders: callFolders}.Folder(storeFolder)
}

// backdropPath is a title's file below the store folder's root.
func backdropPath(root, kind string, id int64) string {
	return path.Join(root, "backdrops", kind, strconv.FormatInt(id, 10)+".jpg")
}

// refreshBackdrops marks titles Streamline says changed as still to fetch.
// The file stays meanwhile, and is downloaded again only when the provider
// now names another image.
func refreshBackdrops(kind string, ids []int64) {
	if len(ids) == 0 {
		return
	}
	args := []any{kind}
	for _, id := range ids {
		args = append(args, id)
	}
	if err := exec(
		`UPDATE backdrops SET fetched_at = NULL, missing_at = NULL, failures = 0
		WHERE kind = ? AND id IN (`+placeholders(len(ids))+`)`, args...,
	); err != nil {
		logFailure("marking backdrops to fetch again", err)
	}
}

// dropOrphanBackdrops forgets the backdrops of titles that left Streamline.
// SQLite hands a deleted title's id to the next one, which would be served
// the old picture.
func dropOrphanBackdrops(store sdk.Folder) error {
	for _, k := range backdropKinds {
		res, err := query(
			`SELECT id FROM backdrops b WHERE kind = ? AND NOT EXISTS (
				SELECT 1 FROM `+k.view+` t WHERE t.id = b.id)`, k.kind)
		if err != nil {
			return err
		}
		for _, r := range res.Rows {
			id := row(r).int(0)
			removeBackdrop(store, k.kind, id)
			if err := exec(
				`DELETE FROM backdrops WHERE kind = ? AND id = ?`, k.kind, id,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeBackdrop(store sdk.Folder, kind string, id int64) {
	err := os.Remove(backdropPath(store.GuestPath, kind, id))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		logFailure("removing a backdrop", err)
	}
}

type backdropTitle struct {
	id, providerID, failures int64
	source                   string
}

// fetchBackdrops brings every title's backdrop up to date until deadline, and
// says whether none is left to fetch. A title a provider failed on is tried
// once a call, so a provider that is down ends the calls after three.
func fetchBackdrops(deadline time.Time) (done bool, err error) {
	store, ok := backdropStore()
	if !ok {
		return true, nil
	}
	if err := dropOrphanBackdrops(store); err != nil {
		return false, err
	}
	// The comparison's lease: two calls fetching side by side would each
	// download the same titles.
	claimed, err := claimScan(deadline.Sub(now()))
	if err != nil || !claimed {
		return false, err
	}
	defer releaseScan()
	p := &providers{}
	retry := false
	for _, k := range backdropKinds {
		var cursor int64
		for {
			res, err := query(
				`SELECT t.id, ifnull(t.`+k.providerID+`, 0), ifnull(b.failures, 0),
					ifnull(b.source, '')
				FROM `+k.view+` t
				LEFT JOIN backdrops b ON b.kind = ? AND b.id = t.id
				WHERE t.id > ? AND (b.id IS NULL OR (b.fetched_at IS NULL
					AND (b.missing_at IS NULL OR b.missing_at < ?)))
				ORDER BY t.id LIMIT ?`,
				k.kind, cursor, now().Add(-backdrop.Recheck).Unix(), backdropsPage)
			if err != nil {
				return false, err
			}
			for _, r := range res.Rows {
				t := backdropTitle{
					id: row(r).int(0), providerID: row(r).int(1),
					failures: row(r).int(2), source: row(r).str(3),
				}
				cursor = t.id
				if !now().Before(deadline) {
					return false, nil
				}
				switch err := p.bring(store, k.kind, t); {
				case errors.Is(err, errStoreWrite):
					// A full store stays full for every title after this one.
					sdk.Log("error", "keeping a backdrop failed: raise store_bytes",
						"error", err.Error())
					return true, nil
				case err != nil:
					logFailure("fetching a backdrop", err)
					retry = retry || !backdrop.GaveUp(t.failures)
				}
			}
			if len(res.Rows) < backdropsPage {
				break
			}
		}
	}
	return !retry, nil
}

var errStoreWrite = errors.New("writing to the store folder")

// providers is one call's dealings with TMDB and TVDB: the pace, and the TVDB
// token, which a login gives.
type providers struct {
	last      time.Time
	tvdbToken string
}

// bring fetches one title's backdrop and records how it went. The error is a
// provider's, or errStoreWrite.
func (p *providers) bring(store sdk.Folder, kind string, t backdropTitle) error {
	img, found, err := p.choose(kind, t.providerID)
	if err != nil {
		return errors.Join(err, recordBackdrop(kind, t, backdropFailed))
	}
	if !found {
		removeBackdrop(store, kind, t.id)
		return recordBackdrop(kind, t, backdropMissing)
	}
	file := backdropPath(store.GuestPath, kind, t.id)
	if _, statErr := os.Stat(file); img.URL != t.source || statErr != nil {
		raw, err := p.get(img.URL, "")
		if err != nil {
			return errors.Join(err, recordBackdrop(kind, t, backdropFailed))
		}
		if err := writeBackdrop(file, raw); err != nil {
			return fmt.Errorf("%w: %w", errStoreWrite, err)
		}
	}
	t.source = img.URL
	return recordBackdrop(kind, t, backdropFetched)
}

func writeBackdrop(file string, raw []byte) error {
	if err := os.MkdirAll(path.Dir(file), 0o700); err != nil {
		return err
	}
	// Written aside first: a request may be sending the old file.
	tmp := file + ".part"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

type backdropOutcome int

const (
	backdropFetched backdropOutcome = iota
	backdropMissing
	backdropFailed
)

func recordBackdrop(kind string, t backdropTitle, o backdropOutcome) error {
	var fetched, missing any
	failures := int64(0)
	switch o {
	case backdropFetched:
		fetched = now().Unix()
	case backdropMissing:
		t.source, missing = "", now().Unix()
	case backdropFailed:
		// A title given up on keeps the picture it had.
		if backdrop.GaveUp(t.failures) {
			missing = now().Unix()
		} else {
			failures = t.failures + 1
		}
	}
	return exec(
		`INSERT INTO backdrops(kind, id, source, fetched_at, missing_at, failures)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(kind, id) DO UPDATE SET source = excluded.source,
			fetched_at = excluded.fetched_at, missing_at = excluded.missing_at,
			failures = excluded.failures`,
		kind, t.id, t.source, fetched, missing, failures)
}

// choose asks the providers for a title's backdrop. found is false when none
// of them has one.
func (p *providers) choose(
	kind string, providerID int64,
) (img backdrop.Image, found bool, err error) {
	if providerID == 0 {
		return backdrop.Image{}, false, nil
	}
	id := strconv.FormatInt(providerID, 10)
	if kind == "movies" {
		return p.tmdbImages("/3/movie/" + id + "/images")
	}
	raw, err := p.tmdb("/3/find/" + id + "?external_source=tvdb_id")
	if err != nil {
		return backdrop.Image{}, false, err
	}
	tmdbID, known, err := backdrop.TMDBFind(raw)
	if err != nil {
		return backdrop.Image{}, false, err
	}
	if known {
		img, found, err = p.tmdbImages(
			"/3/tv/" + strconv.FormatInt(tmdbID, 10) + "/images")
		if err != nil || found {
			return img, found, err
		}
	}
	return p.tvdbImages(id)
}

func (p *providers) tmdbImages(path string) (backdrop.Image, bool, error) {
	raw, err := p.tmdb(path)
	if errors.Is(err, errNoRecord) {
		return backdrop.Image{}, false, nil
	}
	if err != nil {
		return backdrop.Image{}, false, err
	}
	images, err := backdrop.TMDBImages(raw, tmdbImageURL())
	if err != nil {
		return backdrop.Image{}, false, err
	}
	img, found := backdrop.Pick(images)
	return img, found, nil
}

func (p *providers) tmdb(path string) ([]byte, error) {
	return p.get(tmdbURL()+path, fileSecrets[tmdbSecretName])
}

func (p *providers) tvdbImages(seriesID string) (backdrop.Image, bool, error) {
	key := fileSecrets[tvdbSecretName]
	if key == "" {
		return backdrop.Image{}, false, nil
	}
	if p.tvdbToken == "" {
		body, err := json.Marshal(map[string]string{"apikey": key})
		if err != nil {
			return backdrop.Image{}, false, err
		}
		raw, err := p.fetch(sdk.FetchRequest{
			Method:  http.MethodPost,
			URL:     tvdbURL() + "/v4/login",
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    body,
		})
		if err != nil {
			return backdrop.Image{}, false, err
		}
		if p.tvdbToken, err = backdrop.TVDBToken(raw); err != nil {
			return backdrop.Image{}, false, err
		}
	}
	raw, err := p.get(
		tvdbURL()+"/v4/series/"+seriesID+"/artworks?type=3", p.tvdbToken)
	if errors.Is(err, errNoRecord) {
		return backdrop.Image{}, false, nil
	}
	if err != nil {
		return backdrop.Image{}, false, err
	}
	images, err := backdrop.TVDBImages(raw)
	if err != nil {
		return backdrop.Image{}, false, err
	}
	img, found := backdrop.Pick(images)
	return img, found, nil
}

// errNoRecord is a provider's 404: it does not know the title.
var errNoRecord = errors.New("the provider has no such record")

func (p *providers) get(url, bearer string) ([]byte, error) {
	req := sdk.FetchRequest{Method: http.MethodGet, URL: url}
	if bearer != "" {
		req.Headers = map[string][]string{"Authorization": {"Bearer " + bearer}}
	}
	return p.fetch(req)
}

func (p *providers) fetch(req sdk.FetchRequest) ([]byte, error) {
	if wait := providerInterval - now().Sub(p.last); wait > 0 {
		time.Sleep(wait)
	}
	p.last = now()
	res, err := sdk.Fetch(req)
	if err != nil {
		return nil, err
	}
	switch {
	case res.Status == http.StatusNotFound:
		return nil, errNoRecord
	case res.Status != http.StatusOK:
		return nil, fmt.Errorf("%s answered %d", req.URL, res.Status)
	case len(res.Body) == 0:
		return nil, fmt.Errorf("%s answered nothing", req.URL)
	}
	return res.Body, nil
}

// bringUpToDate is one call's share of a scan: the comparison of scope, then
// the backdrops, in what budget is left. done false asks for another call.
func bringUpToDate(scope scanScope, budget time.Duration) (done bool, err error) {
	deadline := now().Add(budget)
	if done, err = compare(scope, budget); err != nil || !done {
		return done, err
	}
	return fetchBackdrops(deadline)
}
