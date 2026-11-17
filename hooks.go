package main

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/settings"
)

func init() {
	hooks = func(r sdk.Request) sdk.Response {
		if r.Path == "/hooks/settings" && r.Method == http.MethodPost {
			return settingsHook(r)
		}
		return libraryHook(r)
	}
}

// settingsHook is what Streamline calls before the extension runs with what
// its config file gives it. The answer's text is the reason an admin reads
// when it is refused, so it never quotes a secret.
func settingsHook(r sdk.Request) sdk.Response {
	refuse := func(reason string) sdk.Response {
		return sdk.Response{
			Status: http.StatusUnprocessableEntity, Body: []byte(reason),
		}
	}
	for _, key := range slices.Sorted(maps.Keys(r.Settings)) {
		owner, known := settings.Owner(key)
		if !known {
			return refuse("settings: " + key + " is not a setting of the player")
		}
		if owner != settings.OwnerPlayer {
			return refuse("settings: " + key + " belongs in the engine's config")
		}
		var v any
		if json.Unmarshal(r.Settings[key], &v) != nil {
			return refuse("settings: " + key + " is not a JSON value")
		}
		if err := settings.Validate(key, v); err != nil {
			return refuse("settings: " + key + ": " + err.Error())
		}
	}
	for name := range r.Secrets {
		if name != engineSecretName {
			return refuse("secret_files: " + name + " is not a secret of the player")
		}
	}
	if raw, ok := r.Settings[engineURLKey]; ok {
		var base string
		_ = json.Unmarshal(raw, &base)
		u, err := url.Parse(base)
		if err != nil || !slices.Contains(r.Hosts, strings.ToLower(u.Host)) {
			return refuse(
				"settings: " + engineURLKey + " names a host that hosts does not list",
			)
		}
	}
	return sdk.Response{Status: http.StatusNoContent}
}

// libraryHook is what Streamline calls after an import. Only the host reaches
// it, so it checks no credential.
func libraryHook(r sdk.Request) sdk.Response {
	if r.Path != "/hooks/library" || r.Method != http.MethodPost {
		return notFoundHandler(r)
	}
	var body struct {
		scanScope
		// Series are titles whose own data changed. Their files are named
		// through Episodes, so there is nothing to compare for them.
		Series []int64 `json:"series"`
	}
	if json.Unmarshal(r.Body, &body) != nil {
		return fail(http.StatusBadRequest, "invalid_request", "Invalid request")
	}
	body.Full = slices.DeleteFunc(body.Full, func(f string) bool {
		return f != "movies" && f != "series"
	})
	if body.empty() {
		if len(body.Series) > 0 {
			return jsonOK(http.StatusAccepted, struct{}{})
		}
		return fail(http.StatusBadRequest, "invalid_request", "Nothing to compare")
	}
	rememberFolders(r)
	done, err := compare(body.scanScope, compareBudget)
	if err != nil {
		return libraryFailed(err)
	}
	noteUnfinished(done)
	return jsonOK(http.StatusAccepted, struct{}{})
}
