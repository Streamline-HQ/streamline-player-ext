package main

import (
	"encoding/json"
	"net/http"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/settings"
)

func init() {
	registerSchema(
		`CREATE TABLE IF NOT EXISTS settings(
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		) WITHOUT ROWID`,
	)
}

// fileSettings and fileSecrets are what Streamline's config file gives this
// extension, as the request in hand carried them. A guest instance serves one
// request, so they never outlive it.
var (
	fileSettings map[string]json.RawMessage
	fileSecrets  map[string]string
)

// playerLocked says whether the config file holds a player setting. It then
// holds all of them: the ones it leaves out are unset, and none can be
// changed from an app.
func playerLocked() bool {
	for key := range fileSettings {
		if owner, _ := settings.Owner(key); owner == settings.OwnerPlayer {
			return true
		}
	}
	return false
}

func readOnlyPlayer() sdk.Response {
	return fail(
		http.StatusConflict, "read_only",
		"Streamline's config file holds the player's settings",
		"owner", settings.OwnerPlayer,
	)
}

// setting reads the JSON value given under key, by the config file first and
// the database otherwise, and reports whether there was one that fits T.
func setting[T any](key string) (T, bool) {
	var v T
	if raw, ok := fileSettings[key]; ok {
		if err := json.Unmarshal(raw, &v); err != nil {
			var zero T
			return zero, false
		}
		return v, true
	}
	if owner, _ := settings.Owner(key); owner == settings.OwnerPlayer &&
		playerLocked() {
		return v, false
	}
	res, err := query(`SELECT value FROM settings WHERE key = ?`, key)
	if err != nil {
		sdk.Log("warn", "reading a setting failed", "key", key, "error", err.Error())
		return v, false
	}
	if len(res.Rows) == 0 {
		return v, false
	}
	if err := json.Unmarshal([]byte(row(res.Rows[0]).str(0)), &v); err != nil {
		var zero T
		return zero, false
	}
	return v, true
}

func putSetting(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return exec(
		`INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, string(raw),
	)
}

func settingStrings(key string) []string {
	v, _ := setting[[]string](key)
	return v
}
