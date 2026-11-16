package main

import (
	"encoding/json"

	"github.com/datahearth/streamline/extensions/sdk"
)

func init() {
	registerSchema(
		`CREATE TABLE IF NOT EXISTS settings(
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		) WITHOUT ROWID`,
	)
}

// setting reads the JSON value stored under key and reports whether there was
// one that fits T.
func setting[T any](key string) (T, bool) {
	var v T
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
