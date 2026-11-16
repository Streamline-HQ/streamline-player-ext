package main

import (
	"net/http"
	"os"
	"path"
	"regexp"

	"github.com/datahearth/streamline/extensions/sdk"
)

var (
	posterKinds = map[string]bool{
		"movies": true, "series": true, "seasons": true, "episodes": true,
		"people": true,
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
	if !posterKinds[kind] || !posterID.MatchString(id) ||
		!posterFile.MatchString(file) {
		return libraryNotFound()
	}
	folder, ok := c.R.Folder("artwork")
	if !ok {
		return libraryNotFound()
	}
	hostPath := path.Join(folder.HostPath, kind, id, file)
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
