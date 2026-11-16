package main

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/tokens"
)

const (
	pairingTTL      = 600 * time.Second
	pairingInterval = 5 * time.Second
	pairingKeep     = time.Hour

	pairPending  = "pending"
	pairApproved = "approved"
	pairDenied   = "denied"
)

func init() {
	registerSchema(
		`CREATE TABLE IF NOT EXISTS pairings(
			device_code_hash TEXT PRIMARY KEY,
			user_code TEXT NOT NULL UNIQUE,
			client_id TEXT NOT NULL,
			name TEXT NOT NULL,
			platform TEXT NOT NULL,
			ip TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			state TEXT NOT NULL DEFAULT 'pending',
			user_id INTEGER NOT NULL DEFAULT 0,
			last_poll INTEGER NOT NULL DEFAULT 0
		) WITHOUT ROWID`,
	)
	handleRoute(http.MethodPost, "/auth/device/code", Public, postPairing)
	handleRoute(http.MethodPost, "/auth/device/token", Public, postPairingToken)
	handleRoute(
		http.MethodGet,
		"/auth/device/{userCode}",
		BearerOnly,
		getPairRequest,
	)
	handleRoute(
		http.MethodPost, "/auth/device/{userCode}/approve", BearerOnly, approveOwn,
	)
	handleRoute(
		http.MethodPost, "/auth/device/{userCode}/deny", BearerOnly, denyPairing,
	)
	handleRoute(http.MethodGet, "/admin/pairings", Admin, listPairings)
	handleRoute(
		http.MethodPost, "/admin/pairings/{userCode}/approve", Admin, approveFor,
	)
	handleRoute(
		http.MethodPost, "/admin/pairings/{userCode}/deny", Admin, denyPairing,
	)
	handleRoute(http.MethodGet, "/admin/users", Admin, listUsers)
}

var errNoUserCode = errors.New("no free user code after 5 tries")

func dashed(code string) string { return code[:3] + "-" + code[3:] }

func noPendingPairing() sdk.Response {
	return fail(
		http.StatusNotFound,
		"no_pending_pairing",
		"No pending pairing for that code",
	)
}

func verificationURI(r sdk.Request) string {
	var base string
	if _, err := setting("player.url", &base); err != nil || base == "" {
		base = r.Origin + r.BasePath
	}
	return strings.TrimRight(base, "/") + "/pair"
}

type pairingStart struct {
	ClientID string `json:"client_id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

func postPairing(c *call) sdk.Response {
	var req pairingStart
	if err := decodeBody(c, &req); err != nil {
		return fail(http.StatusBadRequest, "invalid_request", "Invalid body")
	}
	req.Name, req.Platform = clip(req.Name), clip(req.Platform)
	if req.ClientID == "" || req.Name == "" || req.Platform == "" {
		return fail(
			http.StatusBadRequest, "invalid_request",
			"client_id, name and platform are required",
		)
	}
	t := now()
	if err := exec(
		`DELETE FROM pairings WHERE expires_at < ?`,
		t.Add(-pairingKeep).Unix(),
	); err != nil {
		return internalError("sweeping pairings", err)
	}

	deviceCode := tokens.New()
	var userCode string
	for range 5 {
		userCode = tokens.UserCode()
		_, err := run(
			`INSERT INTO pairings(device_code_hash, user_code, client_id, name,
				platform, ip, created_at, expires_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			tokens.Hash(deviceCode), userCode, req.ClientID, req.Name, req.Platform,
			c.R.ClientIP, t.Unix(), t.Add(pairingTTL).Unix(),
		)
		if err == nil {
			break
		}
		userCode = ""
	}
	if userCode == "" {
		return internalError("storing a pairing", errNoUserCode)
	}

	uri := verificationURI(c.R)
	return jsonOK(http.StatusOK, map[string]any{
		"device_code":               deviceCode,
		"user_code":                 dashed(userCode),
		"verification_uri":          uri,
		"verification_uri_complete": uri + "?code=" + dashed(userCode),
		"expires_in":                int64(pairingTTL / time.Second),
		"interval":                  int64(pairingInterval / time.Second),
	})
}

func pairingError(code string) sdk.Response {
	return fail(http.StatusBadRequest, code, strings.ReplaceAll(code, "_", " "))
}

func postPairingToken(c *call) sdk.Response {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if err := decodeBody(c, &req); err != nil {
		return fail(http.StatusBadRequest, "invalid_request", "Invalid body")
	}
	hash := tokens.Hash(req.DeviceCode)
	res, err := query(
		`SELECT client_id, name, platform, expires_at, state, user_id, last_poll
		FROM pairings WHERE device_code_hash = ?`, hash,
	)
	if err != nil {
		return internalError("reading a pairing", err)
	}
	if len(res.Rows) == 0 {
		return pairingError("expired_token")
	}
	r := row(res.Rows[0])
	t := now()
	if r.int(3) < t.Unix() {
		return pairingError("expired_token")
	}

	switch r.str(4) {
	case pairDenied:
		if err := exec(
			`DELETE FROM pairings WHERE device_code_hash = ?`, hash,
		); err != nil {
			return internalError("removing a pairing", err)
		}
		return pairingError("access_denied")
	case pairApproved:
		out, err := run(
			`DELETE FROM pairings WHERE device_code_hash = ? AND state = ?`,
			hash, pairApproved,
		)
		if err != nil {
			return internalError("removing a pairing", err)
		}
		if out.RowsAffected != 1 {
			return pairingError("expired_token")
		}
		key, id, err := createAppDevice(
			uint32(r.int(5)), //nolint:gosec // a core user id fits
			r.str(0), r.str(1), r.str(2),
		)
		if err != nil {
			return internalError("creating a device key", err)
		}
		return jsonOK(http.StatusOK, map[string]any{"api_key": key, "key_id": id})
	}

	last := r.int(6)
	if err := exec(
		`UPDATE pairings SET last_poll = ? WHERE device_code_hash = ?`,
		t.Unix(), hash,
	); err != nil {
		return internalError("recording a poll", err)
	}
	if last != 0 && t.Unix()-last < int64(pairingInterval/time.Second) {
		return pairingError("slow_down")
	}
	return pairingError("authorization_pending")
}

// pendingCode is the stored form of a typed user code, or false when it
// cannot be one.
func pendingCode(c *call) (string, bool) {
	return tokens.NormalizeUserCode(c.Params["userCode"])
}

func getPairRequest(c *call) sdk.Response {
	code, ok := pendingCode(c)
	if !ok {
		return noPendingPairing()
	}
	res, err := query(
		`SELECT name, platform, ip, created_at FROM pairings
		WHERE user_code = ? AND state = ? AND expires_at >= ?`,
		code, pairPending, now().Unix(),
	)
	if err != nil {
		return internalError("reading a pairing", err)
	}
	if len(res.Rows) == 0 {
		return noPendingPairing()
	}
	r := row(res.Rows[0])
	return jsonOK(http.StatusOK, map[string]any{
		"user_code":    dashed(code),
		"name":         r.str(0),
		"platform":     r.str(1),
		"ip":           r.str(2),
		"requested_at": isoTime(r.int(3)),
	})
}

func isoTime(unix int64) string {
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

// decide moves a pending pairing to state, for userID when it is approved.
func decide(c *call, state string, userID uint32) sdk.Response {
	code, ok := pendingCode(c)
	if !ok {
		return noPendingPairing()
	}
	out, err := run(
		`UPDATE pairings SET state = ?, user_id = ?
		WHERE user_code = ? AND state = ? AND expires_at >= ?`,
		state, userID, code, pairPending, now().Unix(),
	)
	if err != nil {
		return internalError("deciding a pairing", err)
	}
	if out.RowsAffected != 1 {
		return noPendingPairing()
	}
	return sdk.Response{Status: http.StatusNoContent}
}

func approveOwn(c *call) sdk.Response { return decide(c, pairApproved, c.User.ID) }

func denyPairing(c *call) sdk.Response { return decide(c, pairDenied, 0) }

func approveFor(c *call) sdk.Response {
	var req struct {
		UserID *uint32 `json:"user_id"`
	}
	if err := decodeBody(c, &req); err != nil || req.UserID == nil {
		return fail(http.StatusBadRequest, "invalid_request", "user_id is required")
	}
	res, err := query(
		`SELECT 1 FROM streamline_users WHERE id = ? AND locked = 0`, *req.UserID,
	)
	if err != nil {
		return internalError("reading a user", err)
	}
	if len(res.Rows) == 0 {
		return fail(http.StatusNotFound, "not_found", "No such user")
	}
	return decide(c, pairApproved, *req.UserID)
}

func listPairings(*call) sdk.Response {
	t := now()
	res, err := query(
		`SELECT user_code, name, platform, created_at, expires_at FROM pairings
		WHERE state = ? AND expires_at >= ? ORDER BY created_at`,
		pairPending, t.Unix(),
	)
	if err != nil {
		return internalError("listing pairings", err)
	}
	items := make([]map[string]any, 0, len(res.Rows))
	for _, raw := range res.Rows {
		r := row(raw)
		items = append(items, map[string]any{
			"user_code":  dashed(r.str(0)),
			"name":       r.str(1),
			"platform":   r.str(2),
			"created_at": isoTime(r.int(3)),
			"expires_at": isoTime(r.int(4)),
		})
	}
	return jsonOK(http.StatusOK, map[string]any{"items": items})
}

func listUsers(*call) sdk.Response {
	res, err := query(
		`SELECT u.id, ` + userLabel + `, u.role FROM streamline_users u
		WHERE u.locked = 0 ORDER BY 2 COLLATE NOCASE, u.id`,
	)
	if err != nil {
		return internalError("listing users", err)
	}
	items := make([]map[string]any, 0, len(res.Rows))
	for _, raw := range res.Rows {
		r := row(raw)
		items = append(items, map[string]any{
			"id": r.int(0), "name": r.str(1), "role": roleLabel(r.str(2)),
		})
	}
	return jsonOK(http.StatusOK, map[string]any{"items": items})
}
