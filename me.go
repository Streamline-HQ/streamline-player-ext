package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"unicode"

	"github.com/datahearth/streamline/extensions/sdk"
)

const (
	defaultLibraryLanguage = "en"
	// pictureMax sits below core's 1 MiB request cap (middleware.BodyLimit),
	// which answers a bigger body itself before the extension runs.
	pictureMax = 512 << 10
)

func init() {
	registerSchema(
		`CREATE TABLE IF NOT EXISTS accounts(
			user_id INTEGER PRIMARY KEY,
			library_language TEXT NOT NULL DEFAULT 'en',
			picture BLOB,
			picture_type TEXT NOT NULL DEFAULT ''
		)`,
	)
	handleRoute(http.MethodGet, "/me", Any, getMe)
	handleRoute(http.MethodPut, "/me/picture", Any, putPicture)
	handleRoute(http.MethodDelete, "/me/picture", Any, deletePicture)
	handleRoute(http.MethodGet, "/posters/users/me/photo.jpg", Any, getPicture)
}

func getMe(c *call) sdk.Response {
	language, hasPicture := defaultLibraryLanguage, false
	res, err := query(
		`SELECT library_language, picture IS NOT NULL FROM accounts
		WHERE user_id = ?`, c.User.ID,
	)
	if err != nil {
		return internalError("reading the account", err)
	}
	if len(res.Rows) == 1 {
		r := row(res.Rows[0])
		language, hasPicture = r.str(0), r.int(1) != 0
	}
	return jsonOK(http.StatusOK, map[string]any{
		"id":               c.User.ID,
		"name":             c.User.Name,
		"email":            c.User.Email,
		"role":             roleLabel(c.User.Role),
		"library_language": language,
		"picture":          hasPicture,
		"initials":         initials(c.User),
		"streamline_url":   c.R.Origin,
	})
}

// initials is the first letters of the first and last word of the name, in
// capitals. An account with no display name falls back to its email.
func initials(u *User) string {
	words := strings.Fields(u.Name)
	if len(words) == 0 {
		local, _, _ := strings.Cut(u.Email, "@")
		words = strings.FieldsFunc(local, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
	}
	if len(words) == 0 {
		return ""
	}
	first := []rune(words[0])[0]
	if len(words) == 1 {
		return strings.ToUpper(string(first))
	}
	last := []rune(words[len(words)-1])[0]
	return strings.ToUpper(string([]rune{first, last}))
}

var (
	magicJPEG = []byte{0xFF, 0xD8, 0xFF}
	magicPNG  = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}
)

// sniffPicture names the image type of data by its magic bytes, or "".
func sniffPicture(data []byte) string {
	switch {
	case bytes.HasPrefix(data, magicJPEG):
		return "image/jpeg"
	case bytes.HasPrefix(data, magicPNG):
		return "image/png"
	case len(data) >= 12 && string(data[:4]) == "RIFF" &&
		string(data[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

func putPicture(c *call) sdk.Response {
	if len(c.R.Body) > pictureMax {
		return fail(http.StatusRequestEntityTooLarge, "too_large",
			"The picture is over 512 KB.")
	}
	kind := sniffPicture(c.R.Body)
	if kind == "" {
		return fail(http.StatusUnsupportedMediaType, "unsupported_type",
			"The picture must be a JPEG, PNG or WebP image.")
	}
	if err := exec(
		`INSERT INTO accounts(user_id, picture, picture_type)
		VALUES(?, unhex(?), ?)
		ON CONFLICT(user_id) DO UPDATE SET
			picture = excluded.picture, picture_type = excluded.picture_type`,
		c.User.ID, hex.EncodeToString(c.R.Body), kind,
	); err != nil {
		return internalError("saving the picture", err)
	}
	return sdk.Response{Status: http.StatusNoContent}
}

func deletePicture(c *call) sdk.Response {
	if err := exec(
		`UPDATE accounts SET picture = NULL, picture_type = '' WHERE user_id = ?`,
		c.User.ID,
	); err != nil {
		return internalError("removing the picture", err)
	}
	return sdk.Response{Status: http.StatusNoContent}
}

func getPicture(c *call) sdk.Response {
	res, err := query(
		`SELECT picture, picture_type FROM accounts
		WHERE user_id = ? AND picture IS NOT NULL`, c.User.ID,
	)
	if err != nil {
		return internalError("reading the picture", err)
	}
	if len(res.Rows) == 0 {
		return fail(http.StatusNotFound, "not_found", "No picture")
	}
	r := row(res.Rows[0])
	data, err := base64.StdEncoding.DecodeString(r.str(0))
	if err != nil {
		return internalError("decoding the picture", err)
	}
	return sdk.Response{
		Status: http.StatusOK,
		Headers: map[string][]string{
			"Content-Type":  {r.str(1)},
			"Cache-Control": {"private, no-cache"},
		},
		Body: data,
	}
}
