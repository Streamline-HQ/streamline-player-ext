package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/envelope"
)

var errEngineNotConfigured = errors.New("the engine is not configured")

// setting names are read here so a typo is one place.
const (
	engineURLKey       = "engine.url"
	enginePublicURLKey = "engine.public_url"
)

func trimmedURL(key string) (string, bool) {
	u, _ := setting[string](key)
	u = strings.TrimRight(u, "/")
	return u, u != ""
}

// publicEngineURL is where players reach the engine's /hls and /dl routes.
func publicEngineURL() (string, bool) { return trimmedURL(enginePublicURLKey) }

// engineFetch calls the engine's control plane and returns the status and body
// of whatever answered. err is set only when nothing did: the engine has no
// URL or secret yet, or the call did not get through.
func engineFetch(
	method, path string,
	in any,
) (status int, raw []byte, err error) {
	base, ok := trimmedURL(engineURLKey)
	secret, hasSecret := engineSecret()
	if !ok || !hasSecret {
		return 0, nil, errEngineNotConfigured
	}
	req := sdk.FetchRequest{
		Method: method,
		URL:    base + path,
		Headers: map[string][]string{
			"Authorization": {"Bearer " + secret},
		},
	}
	if in != nil {
		if req.Body, err = json.Marshal(in); err != nil {
			return 0, nil, err
		}
		req.Headers["Content-Type"] = []string{"application/json"}
	}
	res, err := sdk.Fetch(req)
	if err != nil {
		return 0, nil, err
	}
	if res.Error != "" {
		return 0, nil, errors.New(res.Error)
	}
	return res.Status, res.Body, nil
}

// engineDo is engineFetch for a caller that wants the answer decoded into out
// when the engine says 2xx. A failing status is returned as it came; use
// engineFetch and engineError when the body is needed to answer it.
func engineDo(method, path string, in, out any) (status int, err error) {
	status, raw, err := engineFetch(method, path, in)
	if err != nil {
		return 0, err
	}
	if status/100 == 2 && out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return 0, err
		}
	}
	return status, nil
}

// engineError is the envelope answer for an engine failure. A status of 0
// stands for no answer at all.
func engineError(status int, raw []byte) sdk.Response {
	status, body := envelope.FromEngine(status, raw)
	return jsonResponse(status, body)
}

func engineUnavailable() sdk.Response {
	return engineError(0, nil)
}
