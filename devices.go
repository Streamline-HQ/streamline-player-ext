package main

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"

	"github.com/datahearth/streamline/extensions/sdk"
)

const heartbeatSilence = 90 // seconds

func init() {
	handleRoute(http.MethodGet, "/devices", Any, listDevices)
	handleRoute(http.MethodDelete, "/devices/current", Any, signOutDevice)
	handleRoute(http.MethodDelete, "/devices/{deviceId}", Any, revokeOwnDevice)
}

const deviceColumns = `d.id, d.kind, d.name, d.platform,
	MAX(d.last_seen, d.created_at),
	(SELECT MAX(t.expires_at) FROM refresh_tokens t
		WHERE t.device_id = d.id AND t.rotated = 0)`

const deviceFilter = `FROM devices d WHERE d.user_id = ?
	AND (d.kind = 'app' OR EXISTS(SELECT 1 FROM refresh_tokens t
		WHERE t.device_id = d.id AND t.rotated = 0 AND t.expires_at > ?))`

type listedDevice struct {
	id, lastSeen int64
	item         map[string]any
}

// sortDevices puts the caller's own device first, then the ones streaming,
// then the rest by last use.
func sortDevices(rows []listedDevice) {
	slices.SortFunc(rows, func(a, b listedDevice) int {
		return cmp.Or(
			-cmp.Compare(boolRank(a.item["current"]), boolRank(b.item["current"])),
			-cmp.Compare(
				boolRank(a.item["streaming"]),
				boolRank(b.item["streaming"]),
			),
			-cmp.Compare(a.lastSeen, b.lastSeen),
			-cmp.Compare(a.id, b.id),
		)
	})
}

func boolRank(v any) int {
	if b, _ := v.(bool); b {
		return 1
	}
	return 0
}

// hasSessionsTable is false until the playback area creates its table.
func hasSessionsTable() (bool, error) {
	res, err := query(
		`SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`,
	)
	return err == nil && len(res.Rows) == 1, err
}

func streamingDevices(userID uint32) (map[int64]bool, error) {
	ok, err := hasSessionsTable()
	if err != nil || !ok {
		return nil, err
	}
	res, err := query(
		`SELECT DISTINCT s.device_id FROM sessions s
		WHERE s.user_id = ? AND s.last_beat > ?
			AND COALESCE(s.stopped_code, '') = ''`,
		userID, now().Unix()-heartbeatSilence,
	)
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, r := range res.Rows {
		out[row(r).int(0)] = true
	}
	return out, nil
}

func listDevices(c *call) sdk.Response {
	res, err := query(
		`SELECT `+deviceColumns+` `+deviceFilter, c.User.ID, now().Unix(),
	)
	if err != nil {
		return internalError("listing devices", err)
	}
	streaming, err := streamingDevices(c.User.ID)
	if err != nil {
		sdk.Log("warn", "reading live sessions failed", "error", err.Error())
	}
	rows := make([]listedDevice, 0, len(res.Rows))
	for _, raw := range res.Rows {
		r := row(raw)
		id := r.int(0)
		item := map[string]any{
			"id":        strconv.FormatInt(id, 10),
			"kind":      r.str(1),
			"name":      r.str(2),
			"platform":  r.str(3),
			"last_seen": isoTime(r.int(4)),
			"streaming": streaming[id],
			"current":   id == c.Device.ID,
		}
		if r.str(1) == kindBrowser {
			if item["platform"] == "" {
				item["platform"] = "Web browser"
			}
			item["expires_at"] = isoTime(r.int(5))
		}
		rows = append(rows, listedDevice{id, r.int(4), item})
	}
	sortDevices(rows)
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		items = append(items, r.item)
	}
	return jsonOK(http.StatusOK, map[string]any{"items": items})
}

func signOutDevice(c *call) sdk.Response {
	if err := revokeDevice(c.Device.ID); err != nil {
		return internalError("revoking the device", err)
	}
	return sdk.Response{Status: http.StatusNoContent}
}

func revokeOwnDevice(c *call) sdk.Response {
	id, err := strconv.ParseInt(c.Params["deviceId"], 10, 64)
	if err != nil {
		return fail(http.StatusNotFound, "not_found", "No such device")
	}
	res, err := query(
		`SELECT 1 FROM devices WHERE id = ? AND user_id = ?`, id, c.User.ID,
	)
	if err != nil {
		return internalError("reading a device", err)
	}
	if len(res.Rows) == 0 {
		return fail(http.StatusNotFound, "not_found", "No such device")
	}
	if err := revokeDevice(id); err != nil {
		return internalError("revoking the device", err)
	}
	return sdk.Response{Status: http.StatusNoContent}
}
