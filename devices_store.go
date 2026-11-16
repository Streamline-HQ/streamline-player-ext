package main

import (
	"strconv"
	"time"

	"github.com/streamline-hq/streamline-player-ext/tokens"
)

const (
	kindApp     = "app"
	kindBrowser = "browser"

	accessTTL  = 900 * time.Second
	refreshTTL = 30 * 24 * time.Hour
)

func init() {
	registerSchema(
		`CREATE TABLE IF NOT EXISTS devices(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			kind TEXT NOT NULL,
			client_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			platform TEXT NOT NULL DEFAULT '',
			key_hash TEXT UNIQUE,
			created_at INTEGER NOT NULL,
			last_seen INTEGER NOT NULL DEFAULT 0,
			last_ip TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS devices_user ON devices(user_id, client_id)`,
		`CREATE TABLE IF NOT EXISTS access_tokens(
			hash TEXT PRIMARY KEY,
			device_id INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS access_tokens_device
			ON access_tokens(device_id)`,
		`CREATE TABLE IF NOT EXISTS refresh_tokens(
			hash TEXT PRIMARY KEY,
			device_id INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			rotated INTEGER NOT NULL DEFAULT 0
		) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS refresh_tokens_device
			ON refresh_tokens(device_id)`,
	)
}

// Tokens is the contract's Tokens: what a browser holds after signing in.
type Tokens struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
	DeviceID         string `json:"device_id"`
}

// onRevoke runs for every device revokeDevice removes, so the areas that tie
// state to a device (playback sessions) can drop it.
var onRevoke []func(deviceID int64) error

// createAppDevice makes the device key of a native app and returns it, the
// only time it exists in the clear. Any older key of the same app for the same
// account is revoked.
func createAppDevice(
	userID uint32,
	clientID, name, platform string,
) (key string, keyID int64, err error) {
	res, err := query(
		`SELECT id FROM devices WHERE user_id = ? AND client_id = ? AND kind = ?`,
		userID, clientID, kindApp,
	)
	if err != nil {
		return "", 0, err
	}
	for _, r := range res.Rows {
		if err := revokeDevice(row(r).int(0)); err != nil {
			return "", 0, err
		}
	}
	key = tokens.New()
	out, err := run(
		`INSERT INTO devices(user_id, kind, client_id, name, platform, key_hash,
			created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		userID, kindApp, clientID, name, platform, tokens.Hash(key),
		now().Unix(),
	)
	if err != nil {
		return "", 0, err
	}
	return key, out.LastInsertID, nil
}

// createBrowser registers a browser; issueBrowserTokens gives it tokens.
func createBrowser(userID uint32, name string) (*Device, error) {
	out, err := run(
		`INSERT INTO devices(user_id, kind, name, created_at) VALUES(?, ?, ?, ?)`,
		userID, kindBrowser, name, now().Unix(),
	)
	if err != nil {
		return nil, err
	}
	return &Device{
		ID: out.LastInsertID, Kind: kindBrowser, UserID: userID, Name: name,
	}, nil
}

// issueBrowserTokens makes a fresh access and refresh pair for a browser.
func issueBrowserTokens(d *Device) (Tokens, error) {
	access, refresh := tokens.New(), tokens.New()
	at := now()
	for _, table := range []string{"access_tokens", "refresh_tokens"} {
		if err := exec(
			`DELETE FROM `+table+` WHERE device_id = ? AND expires_at < ?`,
			d.ID, at.Unix(),
		); err != nil {
			return Tokens{}, err
		}
	}
	if _, err := run(
		`INSERT INTO access_tokens(hash, device_id, expires_at) VALUES(?, ?, ?)`,
		tokens.Hash(access), d.ID, at.Add(accessTTL).Unix(),
	); err != nil {
		return Tokens{}, err
	}
	if _, err := run(
		`INSERT INTO refresh_tokens(hash, device_id, expires_at) VALUES(?, ?, ?)`,
		tokens.Hash(refresh), d.ID, at.Add(refreshTTL).Unix(),
	); err != nil {
		return Tokens{}, err
	}
	return Tokens{
		AccessToken:      access,
		TokenType:        "Bearer",
		ExpiresIn:        int64(accessTTL / time.Second),
		RefreshToken:     refresh,
		RefreshExpiresIn: int64(refreshTTL / time.Second),
		DeviceID:         strconv.FormatInt(d.ID, 10),
	}, nil
}

// revokeDevice removes a device: its key, or every token of a browser. What
// is tied to it elsewhere goes through onRevoke.
func revokeDevice(id int64) error {
	for _, stmt := range []string{
		`DELETE FROM access_tokens WHERE device_id = ?`,
		`DELETE FROM refresh_tokens WHERE device_id = ?`,
		`DELETE FROM devices WHERE id = ?`,
	} {
		if err := exec(stmt, id); err != nil {
			return err
		}
	}
	for _, hook := range onRevoke {
		if err := hook(id); err != nil {
			return err
		}
	}
	return nil
}
