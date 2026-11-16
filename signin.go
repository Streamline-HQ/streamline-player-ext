package main

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/redirect"
	"github.com/streamline-hq/streamline-player-ext/tokens"
)

const (
	authCodeTTL      = 60 * time.Second
	registerCacheTTL = time.Hour
	maxDeviceName    = 64
	registerCacheKey = "auth.register_url"
)

func init() {
	registerSchema(
		`CREATE TABLE IF NOT EXISTS auth_codes(
			hash TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			redirect_uri TEXT NOT NULL,
			challenge TEXT NOT NULL,
			expires_at INTEGER NOT NULL
		) WITHOUT ROWID`,
	)
	authorize = serveAuthorize
	handleRoute(http.MethodGet, "/auth/methods", Public, getAuthMethods)
	handleRoute(http.MethodPost, "/auth/token", Public, postToken)
}

func classifyRedirect(uri string) redirect.Kind {
	return redirect.Classify(
		uri,
		settingStrings("player.origins"),
		settingStrings("player.app_links"),
	)
}

// serveAuthorize is the /authorize mount. Streamline has signed the caller in
// by the time it runs; it sends the browser back with a one-time code.
func serveAuthorize(r sdk.Request) sdk.Response {
	if r.Method != http.MethodGet {
		return fail(
			http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed",
		)
	}
	q, err := url.ParseQuery(r.Query)
	if err != nil {
		return fail(http.StatusBadRequest, "invalid_request", "Invalid query")
	}
	uri, state, challenge := q.Get(
		"redirect_uri",
	), q.Get(
		"state",
	), q.Get(
		"code_challenge",
	)
	if classifyRedirect(uri) == redirect.Refused {
		return fail(
			http.StatusBadRequest, "invalid_request", "redirect_uri is not allowed",
		)
	}
	if state == "" || challenge == "" || q.Get("code_challenge_method") != "S256" {
		return fail(
			http.StatusBadRequest, "invalid_request",
			"state and an S256 code_challenge are required",
		)
	}
	if r.Caller == nil {
		return fail(http.StatusUnauthorized, "unauthorized", "Sign in first")
	}

	t := now()
	if err := exec(
		`DELETE FROM auth_codes WHERE expires_at < ?`, t.Unix(),
	); err != nil {
		return internalError("sweeping authorization codes", err)
	}
	code := tokens.New()
	if err := exec(
		`INSERT INTO auth_codes(hash, user_id, redirect_uri, challenge, expires_at)
		VALUES(?, ?, ?, ?, ?)`,
		tokens.Hash(code), r.Caller.ID, uri, challenge, t.Add(authCodeTTL).Unix(),
	); err != nil {
		return internalError("storing an authorization code", err)
	}

	target, err := url.Parse(uri)
	if err != nil {
		return fail(http.StatusBadRequest, "invalid_request", "Invalid redirect_uri")
	}
	params := target.Query()
	params.Set("code", code)
	params.Set("state", state)
	target.RawQuery = params.Encode()
	return sdk.Response{
		Status: http.StatusFound,
		Headers: map[string][]string{
			"Location":      {target.String()},
			"Cache-Control": {"no-store"},
		},
	}
}

func internalError(what string, err error) sdk.Response {
	sdk.Log("error", what+" failed", "error", err.Error())
	return fail(http.StatusInternalServerError, "internal", "Internal error")
}

type cachedRegister struct {
	At     int64  `json:"at"`
	Origin string `json:"origin"`
	URL    string `json:"url"`
}

func getAuthMethods(c *call) sdk.Response {
	var registerURL *string
	if u := registerURLFor(c.R.Origin); u != "" {
		registerURL = &u
	}
	return jsonOK(http.StatusOK, map[string]*string{"register_url": registerURL})
}

// registerURLFor is Streamline's register page when it answers, remembered for
// an hour. The one remembered answer belongs to one origin, and a fetch that
// fails (Streamline's own host is not among the extension's hosts) is
// remembered for a minute only, so fixing the config shows within the minute.
func registerURLFor(origin string) string {
	if origin == "" {
		return ""
	}
	var cached cachedRegister
	if ok, err := setting(registerCacheKey, &cached); err == nil && ok &&
		cached.Origin == origin &&
		now().Unix()-cached.At < int64(registerCacheTTL/time.Second) {
		return cached.URL
	}
	found := ""
	page := origin + "/register"
	at := now().Unix()
	res, err := sdk.Fetch(sdk.FetchRequest{Method: http.MethodGet, URL: page})
	switch {
	case err != nil:
		sdk.Log("warn", "fetching the register page failed",
			"error", err.Error())
		at -= int64(registerCacheTTL/time.Second) - 60
	case res.Status == http.StatusOK:
		found = page
	}
	if err := putSetting(
		registerCacheKey,
		cachedRegister{At: at, Origin: origin, URL: found},
	); err != nil {
		sdk.Log("warn", "caching the register page failed", "error", err.Error())
	}
	return found
}

type tokenRequest struct {
	GrantType    string `json:"grant_type"`
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	RedirectURI  string `json:"redirect_uri"`
	DeviceName   string `json:"device_name"`
	ClientID     string `json:"client_id"`
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	RefreshToken string `json:"refresh_token"`
}

func invalidGrant() sdk.Response {
	return fail(http.StatusBadRequest, "invalid_grant", "Invalid grant")
}

func postToken(c *call) sdk.Response {
	var req tokenRequest
	if err := decodeBody(c, &req); err != nil {
		return fail(http.StatusBadRequest, "invalid_request", "Invalid body")
	}
	switch req.GrantType {
	case "authorization_code":
		return exchangeCode(req)
	case "refresh_token":
		return rotateRefresh(req.RefreshToken)
	}
	return fail(
		http.StatusBadRequest, "unsupported_grant_type", "Unsupported grant type",
	)
}

// consumeCode takes the code out of the table before it is judged, so a failed
// attempt burns it too.
func consumeCode(
	code string,
) (userID uint32, uri, challenge string, ok bool, err error) {
	hash := tokens.Hash(code)
	res, err := query(
		`SELECT user_id, redirect_uri, challenge, expires_at FROM auth_codes
		WHERE hash = ?`, hash,
	)
	if err != nil || len(res.Rows) == 0 {
		return 0, "", "", false, err
	}
	out, err := run(`DELETE FROM auth_codes WHERE hash = ?`, hash)
	if err != nil || out.RowsAffected != 1 {
		return 0, "", "", false, err
	}
	r := row(res.Rows[0])
	if r.int(3) < now().Unix() {
		return 0, "", "", false, nil
	}
	userID = uint32(r.int(0)) //nolint:gosec // a core user id fits
	return userID, r.str(1), r.str(2), true, nil
}

func exchangeCode(req tokenRequest) sdk.Response {
	userID, uri, challenge, ok, err := consumeCode(req.Code)
	if err != nil {
		return internalError("reading an authorization code", err)
	}
	if !ok || uri != req.RedirectURI ||
		!tokens.PKCEMatches(req.CodeVerifier, challenge) {
		return invalidGrant()
	}
	kind := classifyRedirect(uri)

	if req.ClientID != "" {
		if kind != redirect.Loopback && kind != redirect.App {
			return invalidGrant()
		}
		if req.Name == "" || req.Platform == "" {
			return fail(
				http.StatusBadRequest,
				"invalid_request",
				"name and platform are required",
			)
		}
		key, id, err := createAppDevice(
			userID, req.ClientID, clip(req.Name), req.Platform,
		)
		if err != nil {
			return internalError("creating a device key", err)
		}
		return jsonOK(http.StatusOK, map[string]any{"api_key": key, "key_id": id})
	}

	if kind != redirect.Web {
		return invalidGrant()
	}
	if req.DeviceName == "" {
		return fail(
			http.StatusBadRequest, "invalid_request", "device_name is required",
		)
	}
	d, err := createBrowser(userID, clip(req.DeviceName))
	if err != nil {
		return internalError("creating a browser", err)
	}
	t, err := issueBrowserTokens(d)
	if err != nil {
		return internalError("issuing tokens", err)
	}
	return jsonOK(http.StatusOK, t)
}

func clip(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > maxDeviceName {
		r = r[:maxDeviceName]
	}
	return string(r)
}

// rotateRefresh trades a refresh token for a new pair. A token already traded
// means two parties hold it, so the browser is revoked.
func rotateRefresh(token string) sdk.Response {
	if token == "" {
		return invalidGrant()
	}
	hash := tokens.Hash(token)
	res, err := query(
		`SELECT device_id, expires_at, rotated FROM refresh_tokens WHERE hash = ?`,
		hash,
	)
	if err != nil {
		return internalError("reading a refresh token", err)
	}
	if len(res.Rows) == 0 {
		return invalidGrant()
	}
	r := row(res.Rows[0])
	deviceID := r.int(0)
	if r.int(2) != 0 {
		return revokeReplayed(deviceID)
	}
	if r.int(1) < now().Unix() {
		return invalidGrant()
	}
	out, err := run(
		`UPDATE refresh_tokens SET rotated = 1 WHERE hash = ? AND rotated = 0`, hash,
	)
	if err != nil {
		return internalError("rotating a refresh token", err)
	}
	if out.RowsAffected != 1 {
		return revokeReplayed(deviceID)
	}
	t, err := issueBrowserTokens(&Device{ID: deviceID})
	if err != nil {
		return internalError("issuing tokens", err)
	}
	return jsonOK(http.StatusOK, t)
}

func revokeReplayed(deviceID int64) sdk.Response {
	if err := revokeDevice(deviceID); err != nil {
		return internalError(
			"revoking a browser "+strconv.FormatInt(deviceID, 10),
			err,
		)
	}
	return invalidGrant()
}
