package main

import (
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"

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
	// Core files a person's picture as it does a title's.
	if kind == "people" && file == "photo.jpg" {
		file = "poster.jpg"
	}
	small := file == thumbFile
	if small {
		file = "poster.jpg"
	}
	folder, ok := c.R.Folder("artwork")
	hostPath := path.Join(folder.HostPath, dir, id, file)
	if n, err := strconv.ParseInt(id, 10, 64); file == backdropFile &&
		kind != "people" && err == nil {
		folder, ok = c.R.Folder(storeFolder)
		hostPath = backdropPath(folder.HostPath, kind, n)
	}
	if !ok {
		return libraryNotFound()
	}
	guest, ok := c.R.GuestPath(hostPath)
	if !ok {
		return libraryNotFound()
	}
	info, err := os.Stat(guest)
	if err != nil || info.IsDir() {
		return libraryNotFound()
	}
	if small {
		if thumb, ok := posterThumb(c, kind, id, guest, info); ok {
			hostPath = thumb
		}
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
