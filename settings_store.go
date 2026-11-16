package main

import "encoding/json"

func init() {
	registerSchema(
		`CREATE TABLE IF NOT EXISTS settings(
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		) WITHOUT ROWID`,
	)
}

// setting reads the JSON value stored under key into v and reports whether
// there was one.
func setting(key string, v any) (bool, error) {
	res, err := query(`SELECT value FROM settings WHERE key = ?`, key)
	if err != nil || len(res.Rows) == 0 {
		return false, err
	}
	return true, json.Unmarshal([]byte(row(res.Rows[0]).str(0)), v)
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
	var out []string
	if _, err := setting(key, &out); err != nil {
		return nil
	}
	return out
}
