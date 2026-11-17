package main

import (
	"net/http"
	"os"
	"path"
	"regexp"

	"github.com/datahearth/streamline/extensions/sdk"
)

var (
	// The contract's kind in the URL, and the folder core keeps that art in:
	// core files a show's poster under tvshows.
	posterDirs = map[string]string{
		"movies": "movies", "series": "tvshows", "people": "people",
	}
	posterFile  = regexp.MustCompile(`^[a-z0-9_-]+\.(jpg|png|webp)$`)
	posterTypes = map[string]string{
		".jpg": "image/jpeg", ".png": "image/png", ".webp": "image/webp",
	}
	posterID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

func init() {
	handleRoute(http.MethodGet, "/posters/{kind}/{id}/{file}", Public, getPoster)
}

func getPoster(c *call) sdk.Response {
	kind, id, file := c.Params["kind"], c.Params["id"], c.Params["file"]
	dir, known := posterDirs[kind]
	if !known || !posterID.MatchString(id) || !posterFile.MatchString(file) {
		return libraryNotFound()
	}
	folder, ok := c.R.Folder("artwork")
	if !ok {
		return libraryNotFound()
	}
	hostPath := path.Join(folder.HostPath, dir, id, file)
	guest, ok := c.R.GuestPath(hostPath)
	if !ok {
		return libraryNotFound()
	}
	if info, err := os.Stat(guest); err != nil || info.IsDir() {
		return libraryNotFound()
	}
	return sdk.Response{
		Headers: map[string][]string{
			"Cache-Control": {"private, max-age=86400"},
		},
		SendFile: &sdk.SendFile{
			Path: hostPath, ContentType: posterTypes[path.Ext(file)],
		},
	}
}
