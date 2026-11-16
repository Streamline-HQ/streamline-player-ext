package envelope

import (
	"encoding/json"
	"net/http"
)

// FromEngine rewrites an engine's failure as the envelope players read. The
// engine answers {"code","message","retry_after"}; the fields move inside
// "error". A body that is not that, or an engine that refused our secret
// (401), says nothing the player can act on, so both become 503
// engine_unavailable.
func FromEngine(status int, raw []byte) (int, []byte) {
	var e struct {
		Code       string `json:"code"`
		Message    string `json:"message"`
		RetryAfter int64  `json:"retry_after"`
	}
	if status < http.StatusBadRequest || status == http.StatusUnauthorized ||
		json.Unmarshal(raw, &e) != nil || e.Code == "" {
		return http.StatusServiceUnavailable, Error(
			"engine_unavailable",
			"The streaming engine is not available",
		)
	}
	var extra []any
	if e.RetryAfter > 0 {
		extra = append(extra, "retry_after", e.RetryAfter)
	}
	return status, Error(e.Code, e.Message, extra...)
}
