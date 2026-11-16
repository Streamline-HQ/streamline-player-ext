package main

import (
	"net/http"
	"strings"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/access"
	"github.com/streamline-hq/streamline-player-ext/tokens"
)

const touchInterval = 60 // seconds between last_seen writes per device

// userLabel is how a person is named where only a name fits: the display name,
// or the email's local part for an account that has none.
const userLabel = `COALESCE(NULLIF(u.display_name, ''),
	(SELECT CASE WHEN instr(l.email, '@') > 0
		THEN substr(l.email, 1, instr(l.email, '@') - 1) ELSE l.email END
	FROM streamline_user_logins l WHERE l.user_id = u.id
	ORDER BY l.email LIMIT 1), '')`

const identityColumns = `d.id, d.kind, d.user_id, d.name, d.platform, d.last_seen,
	` + userLabel + `, u.role, u.locked,
	COALESCE((SELECT l.email FROM streamline_user_logins l
		WHERE l.user_id = u.id ORDER BY l.email LIMIT 1), '')`

func header(r sdk.Request, name string) string {
	for k, v := range r.Headers {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func bearerToken(r sdk.Request) string {
	h := header(r, "Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// authenticate resolves the caller from X-API-Key or Authorization: Bearer and
// checks them against what the route declared. It returns nil, with c.User
// and c.Device set, when the call may go on, and the refusal otherwise. A
// device key wins when both are presented. A locked or missing account is
// refused like a revoked credential.
func authenticate(c *call, level Access) *sdk.Response {
	cred, secret := access.None, ""
	if k := header(c.R, "X-API-Key"); k != "" {
		cred, secret = access.Key, k
	} else if b := bearerToken(c.R); b != "" {
		cred, secret = access.Bearer, b
	}
	if cred == access.None {
		return refuse(access.Decide(level, cred, ""))
	}

	user, device, err := lookupCaller(cred, secret)
	if err != nil {
		sdk.Log("error", "looking up the caller failed", "error", err.Error())
		resp := fail(http.StatusInternalServerError, "internal", "Internal error")
		return &resp
	}
	if user == nil {
		return refuse(http.StatusUnauthorized, "unauthorized")
	}
	if status, code := access.Decide(level, cred, user.Role); code != "" {
		return refuse(status, code)
	}
	touchDevice(device, c.R)
	c.User, c.Device = user, device
	return nil
}

// roleLabel is a role the way the player apps name it: Streamline's `admin` and
// `member` are `Admin` and `Member` there.
func roleLabel(role string) string {
	if role == "" {
		return ""
	}
	return strings.ToUpper(role[:1]) + role[1:]
}

func refuse(status int, code string) *sdk.Response {
	if code == "" {
		return nil
	}
	resp := fail(status, code, refusalMessage(code))
	return &resp
}

func refusalMessage(code string) string {
	switch code {
	case "session_required":
		return "This needs a signed-in browser session, not a device key"
	case "forbidden":
		return "This credential may not do that"
	default:
		return "Sign in required"
	}
}

// lookupCaller finds the device and account behind a credential. Both are nil
// for an unknown secret, an expired token or an account that is gone or
// locked.
func lookupCaller(
	cred access.Cred,
	secret string,
) (*User, *Device, error) {
	var (
		res sdk.SQLResult
		err error
	)
	switch cred {
	case access.Key:
		res, err = query(
			`SELECT `+identityColumns+`
			FROM devices d
			JOIN streamline_users u ON u.id = d.user_id
			WHERE d.key_hash = ?`,
			tokens.Hash(secret),
		)
	case access.Bearer:
		res, err = query(
			`SELECT `+identityColumns+`
			FROM access_tokens t
			JOIN devices d ON d.id = t.device_id
			JOIN streamline_users u ON u.id = d.user_id
			WHERE t.hash = ? AND t.expires_at > ?`,
			tokens.Hash(secret), now().Unix(),
		)
	case access.None:
		return nil, nil, nil
	}
	if err != nil || len(res.Rows) == 0 {
		return nil, nil, err
	}
	r := row(res.Rows[0])
	if r.int(8) != 0 {
		return nil, nil, nil
	}
	device := &Device{
		ID:       r.int(0),
		Kind:     r.str(1),
		UserID:   uint32(r.int(2)), //nolint:gosec // a core user id fits
		Name:     r.str(3),
		Platform: r.str(4),
		LastSeen: r.int(5),
	}
	user := &User{
		ID:    device.UserID,
		Name:  r.str(6),
		Role:  r.str(7),
		Email: r.str(9),
	}
	return user, device, nil
}

// touchDevice records that a device was used, at most once a minute.
func touchDevice(d *Device, r sdk.Request) {
	t := now().Unix()
	if d.LastSeen != 0 && t-d.LastSeen < touchInterval {
		return
	}
	if err := exec(
		`UPDATE devices SET last_seen = ?, last_ip = ? WHERE id = ?`,
		t, r.ClientIP, d.ID,
	); err != nil {
		sdk.Log("warn", "recording device use failed", "error", err.Error())
	}
}
