package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/envelope"
)

//nolint:unused // the areas that read the clock arrive after the skeleton
var now = time.Now

func jsonResponse(status int, body []byte) sdk.Response {
	return sdk.Response{
		Status: status,
		Headers: map[string][]string{
			"Content-Type":  {"application/json; charset=utf-8"},
			"Cache-Control": {"no-store"},
		},
		Body: body,
	}
}

//nolint:unused // the first handlers arrive after the skeleton
func jsonOK(status int, v any) sdk.Response {
	body, err := json.Marshal(v)
	if err != nil {
		sdk.Log("error", "encoding a response failed", "error", err.Error())
		return fail(http.StatusInternalServerError, "internal", "Internal error")
	}
	return jsonResponse(status, body)
}

// fail answers in the error envelope; extra is key, value pairs that sit
// inside "error".
//
//nolint:unparam // the first answer with extra fields arrives after the skeleton
func fail(status int, code, message string, extra ...any) sdk.Response {
	return jsonResponse(status, envelope.Error(code, message, extra...))
}

// decodeBody reads the JSON body into v. The caller answers a failure with
// 400 invalid_request.
//
//nolint:unused // the first handlers arrive after the skeleton
func decodeBody(c *call, v any) error {
	return json.Unmarshal(c.R.Body, v)
}
