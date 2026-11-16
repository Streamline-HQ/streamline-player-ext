package main

import (
	"crypto/sha256"
	"encoding/hex"
)

var schemaStatements []string

// registerSchema adds statements to the extension's schema. Each area calls it
// from its init, so a table lives next to the code that uses it. Every
// statement must be idempotent: ensureSchema runs all of them again whenever
// any one changes.
//
//nolint:unused // the areas register their tables after the skeleton
func registerSchema(stmts ...string) {
	schemaStatements = append(schemaStatements, stmts...)
}

const schemaMetaKey = "schema"

// ensureSchema creates what registerSchema collected. The meta table keeps the
// SHA-256 of every statement, so a request that finds it unchanged runs one
// SELECT and nothing else.
func ensureSchema() error {
	h := sha256.New()
	for _, s := range schemaStatements {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	want := hex.EncodeToString(h.Sum(nil))

	res, err := query(`SELECT value FROM meta WHERE key = ?`, schemaMetaKey)
	if err == nil && len(res.Rows) == 1 && res.Rows[0][0] == want {
		return nil
	}
	if err := exec(
		`CREATE TABLE IF NOT EXISTS meta(
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		) WITHOUT ROWID`,
	); err != nil {
		return err
	}
	for _, s := range schemaStatements {
		if err := exec(s); err != nil {
			return err
		}
	}
	return exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		schemaMetaKey, want,
	)
}
