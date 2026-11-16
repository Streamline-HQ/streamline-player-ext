package main

import (
	"encoding/json"
	"net/http"

	"github.com/datahearth/streamline/extensions/sdk"
)

const (
	haltStopped    = "session_stopped"
	haltRestarting = "server_restarting"
)

// halt is what an admin's stop or an engine restart left on a session for the
// player to learn of on its next call.
type halt struct {
	code, message, by string
	retryUntil        int64
}

// answer is nil when the halt no longer applies: a restart whose retry window
// has passed.
func (h halt) answer(sessionID string) *sdk.Response {
	switch h.code {
	case haltStopped:
		resp := fail(
			http.StatusGone, haltStopped, h.message, "by", h.by,
		)
		endSession(sessionID)
		return &resp
	case haltRestarting:
		left := h.retryUntil - now().Unix()
		if left <= 0 {
			return nil
		}
		resp := fail(
			http.StatusServiceUnavailable, haltRestarting, h.message,
			"by", h.by, "retry_after", left,
		)
		return &resp
	}
	return nil
}

const restartMetaKey = "restart"

type restartWindow struct {
	Until   int64  `json:"until"`
	By      string `json:"by"`
	Message string `json:"message"`
}

// noteRestart remembers an engine restart, so a session started while the
// engine is away hears of it as the ones already running do.
func noteRestart(w restartWindow) error {
	raw, err := json.Marshal(w)
	if err != nil {
		return err
	}
	return exec(
		`INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		restartMetaKey, string(raw),
	)
}

// restartAnswer is nil unless the engine is restarting right now.
func restartAnswer() *sdk.Response {
	res, err := query(`SELECT value FROM meta WHERE key = ?`, restartMetaKey)
	if err != nil || len(res.Rows) == 0 {
		return nil
	}
	var w restartWindow
	if json.Unmarshal([]byte(row(res.Rows[0]).str(0)), &w) != nil {
		return nil
	}
	left := w.Until - now().Unix()
	if left <= 0 {
		return nil
	}
	resp := fail(
		http.StatusServiceUnavailable, haltRestarting, w.Message,
		"by", w.By, "retry_after", left,
	)
	return &resp
}
