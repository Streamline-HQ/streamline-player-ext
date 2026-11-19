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
	stopStraySessions()
}

const (
	// reconcileEvery spaces the comparisons with the engine's own list.
	reconcileEvery   = 60 // seconds
	reconcileKey     = "engine_reconcile_at"
	straySessionsKey = "engine_stray_sessions"
)

// stopStraySessions stops what the engine still runs for nobody: a session
// whose stop never reached it, because the request was cut short or the
// engine did not answer in time. A session is stray only once two comparisons
// found no row for it: startSession creates the engine's session before its
// row, and must not lose it to a comparison made in between.
func stopStraySessions() {
	claimed, err := run(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
		WHERE CAST(meta.value AS INTEGER) <= ?`,
		reconcileKey, now().Unix()+reconcileEvery, now().Unix(),
	)
	if err != nil || claimed.RowsAffected == 0 {
		return
	}
	live := engineSessions()
	if len(live) == 0 {
		return
	}
	res, err := query(`SELECT id FROM sessions`)
	if err != nil {
		logFailure("listing the sessions", err)
		return
	}
	for _, r := range res.Rows {
		delete(live, row(r).str(0))
	}
	seen := map[string]bool{}
	if prev, err := query(
		`SELECT value FROM meta WHERE key = ?`, straySessionsKey,
	); err == nil && len(prev.Rows) == 1 {
		for id := range strings.FieldsSeq(row(prev.Rows[0]).str(0)) {
			seen[id] = true
		}
	}
	waiting := make([]string, 0, len(live))
	stopped := 0
	for id := range live {
		if seen[id] && stopped < sweepBatch {
			stopInEngine(id)
			stopped++
			continue
		}
		waiting = append(waiting, id)
	}
	if err := exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		straySessionsKey, strings.Join(waiting, " "),
	); err != nil {
		logFailure("noting the stray sessions", err)
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
