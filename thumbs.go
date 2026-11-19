package main

import (
	"errors"
	"image"
	"image/jpeg"
	"os"
	"path"

	"golang.org/x/image/draw"

	"github.com/datahearth/streamline/extensions/sdk"
)

const (
	thumbFile    = "poster-w342.jpg"
	thumbWidth   = 342
	thumbQuality = 85
	cacheFolder  = "cache"
)

// posterThumb is a card-sized copy of a poster, kept in the cache folder until
// core replaces the poster. WebKitGTK shrinks a large image to a card without
// mipmaps, which turns into grain on the desktop app. ok is false when the
// poster is no wider than a card or no copy can be made: the poster itself is
// served then.
func posterThumb(
	c *call, kind, id, poster string, posterInfo os.FileInfo,
) (string, bool) {
	cache, ok := c.R.Folder(cacheFolder)
	if !ok {
		return "", false
	}
	rel := path.Join("posters", kind, id+".jpg")
	guest := path.Join(cache.GuestPath, rel)
	if info, err := os.Stat(guest); err == nil &&
		!info.ModTime().Before(posterInfo.ModTime()) {
		return path.Join(cache.HostPath, rel), true
	}
	made, err := writeThumb(poster, guest)
	if err != nil {
		sdk.Log("warn", "making a poster thumbnail failed",
			"poster", poster, "error", err.Error())
		return "", false
	}
	return path.Join(cache.HostPath, rel), made
}

// writeThumb reports false, writing nothing, when the poster is already no
// wider than a thumbnail.
func writeThumb(poster, thumb string) (bool, error) {
	f, err := os.Open(poster) //nolint:gosec // getPoster checked kind, id and name
	if err != nil {
		return false, err
	}
	defer f.Close()
	cfg, err := jpeg.DecodeConfig(f)
	if err != nil {
		return false, err
	}
	if cfg.Width <= thumbWidth {
		return false, nil
	}
	if _, err := f.Seek(0, 0); err != nil {
		return false, err
	}
	src, err := jpeg.Decode(f)
	if err != nil {
		return false, err
	}
	dst := image.NewRGBA(image.Rect(
		0, 0, thumbWidth, cfg.Height*thumbWidth/cfg.Width,
	))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)

	if err := os.MkdirAll(path.Dir(thumb), 0o700); err != nil {
		return false, err
	}
	// Written aside first: another request may be sending the old file.
	tmp, err := os.CreateTemp(path.Dir(thumb), "*.part")
	if err != nil {
		return false, err
	}
	err = jpeg.Encode(tmp, dst, &jpeg.Options{Quality: thumbQuality})
	err = errors.Join(err, tmp.Close())
	if err == nil {
		err = os.Rename(tmp.Name(), thumb)
	}
	if err != nil {
		return false, errors.Join(err, os.Remove(tmp.Name()))
	}
	return true, nil
}
