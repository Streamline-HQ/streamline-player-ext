package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/datahearth/streamline/extensions/sdk"
)

// sweepBatch bounds the engine calls one request makes for other people's
// silent sessions.
const sweepBatch = 4

func init() {
	sweep = sweepSilentSessions
	onRevoke = append(onRevoke, endDeviceSessions)
}

// engineBound says whether the request reaches the engine anyway. Only those
// pay for stopping other people's sessions: an engine that hangs must not
// stall browsing, accounts, posters or the admin settings.
func engineBound(r sdk.Request) bool {
	return strings.HasPrefix(r.Path, "/playback/") ||
		strings.HasPrefix(r.Path, "/downloads") ||
		r.Path == "/admin/streams"
}

// sweepSilentSessions forgets the sessions that went silent and then stops
// them in the engine. The rows go first, so an engine that does not answer
// costs one request, not every request after it.
func sweepSilentSessions(r sdk.Request) {
	if !engineBound(r) {
		return
	}
	res, err := query(
		`SELECT id FROM sessions WHERE last_beat < ? ORDER BY last_beat LIMIT ?`,
		now().Unix()-heartbeatSilence, sweepBatch,
	)
	if err != nil {
		logFailure("looking for silent sessions", err)
		return
	}
	ids := make([]string, 0, len(res.Rows))
	for _, silent := range res.Rows {
		id := row(silent).str(0)
		if err := exec(`DELETE FROM sessions WHERE id = ?`, id); err != nil {
			logFailure("forgetting a silent session", err)
			continue
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		stopInEngine(id)
	}
}

// endDeviceSessions stops what a revoked device was playing.
func endDeviceSessions(deviceID int64) error {
	res, err := query(`SELECT id FROM sessions WHERE device_id = ?`, deviceID)
	if err != nil {
		return err
	}
	for _, r := range res.Rows {
		endSession(row(r).str(0))
	}
	return nil
}

// endSession forgets our row and then stops the engine's session. An engine
// that does not answer is left to its own orphan timeout.
func endSession(id string) {
	if err := exec(`DELETE FROM sessions WHERE id = ?`, id); err != nil {
		logFailure("forgetting a session", err)
	}
	stopInEngine(id)
}

func stopInEngine(id string) {
	if _, err := engineDo(
		http.MethodDelete,
		"/v1/sessions/"+id,
		nil,
		nil,
	); err != nil &&
		!errors.Is(err, errEngineNotConfigured) {
		logFailure("stopping an engine session", err)
	}
}
