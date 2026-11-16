package main

import (
	"net/http"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/tokens"
)

func init() {
	registerSchema(
		`CREATE TABLE IF NOT EXISTS engine_secret(
			id INTEGER PRIMARY KEY CHECK (id = 1),
			secret TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
	)
	handleRoute(http.MethodGet, "/admin/engine/secret", Admin, getEngineSecret)
	handleRoute(http.MethodPost, "/admin/engine/secret", Admin, rotateEngineSecret)
}

// engineSecret is the secret shared with the engine. It is kept readable
// because it signs the playback URLs.
func engineSecret() (string, bool) {
	res, err := query(`SELECT secret FROM engine_secret WHERE id = 1`)
	if err != nil {
		sdk.Log("error", "reading the engine secret failed", "error", err.Error())
		return "", false
	}
	if len(res.Rows) == 0 {
		return "", false
	}
	s := row(res.Rows[0]).str(0)
	return s, s != ""
}

func getEngineSecret(*call) sdk.Response {
	res, err := query(`SELECT created_at FROM engine_secret WHERE id = 1`)
	if err != nil {
		return internalError("reading the engine secret", err)
	}
	if len(res.Rows) == 0 {
		return jsonOK(http.StatusOK, map[string]any{"set": false})
	}
	return jsonOK(http.StatusOK, map[string]any{
		"set": true, "created_at": isoTime(row(res.Rows[0]).int(0)),
	})
}

// rotateEngineSecret replaces any older secret; the answer is the only time
// the secret leaves the extension.
func rotateEngineSecret(*call) sdk.Response {
	secret := tokens.New()
	if err := exec(
		`INSERT INTO engine_secret(id, secret, created_at) VALUES(1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			secret = excluded.secret, created_at = excluded.created_at`,
		secret, now().Unix(),
	); err != nil {
		return internalError("storing the engine secret", err)
	}
	return jsonOK(http.StatusOK, map[string]string{"secret": secret})
}
