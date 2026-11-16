package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/settings"
)

func init() {
	handleRoute(http.MethodGet, "/admin/settings", Admin, getSettings)
	handleRoute(http.MethodPatch, "/admin/settings", Admin, patchSettings)
}

type settingsView struct {
	Values      map[string]any    `json:"values"`
	Owners      map[string]string `json:"owners"`
	ReadOnly    map[string]bool   `json:"read_only"`
	Pending     []string          `json:"pending"`
	RestartKeys []string          `json:"restart_keys"`
}

type engineSettingsAnswer struct {
	ReadOnly    bool           `json:"read_only"`
	Values      map[string]any `json:"values"`
	Pending     []string       `json:"pending"`
	RestartKeys []string       `json:"restart_keys"`
}

// engineSettings reads the engine's own settings. ok is false when the engine
// cannot be reached or does not take our secret.
func engineSettings() (a engineSettingsAnswer, ok bool) {
	status, err := engineDo(http.MethodGet, "/v1/settings", nil, &a)
	return a, err == nil && status == http.StatusOK
}

func buildSettingsView() settingsView {
	v := settingsView{
		Values: map[string]any{},
		Owners: map[string]string{},
		ReadOnly: map[string]bool{
			settings.OwnerEngine: false,
			settings.OwnerPlayer: false,
		},
		Pending:     []string{},
		RestartKeys: []string{},
	}
	for _, name := range settings.Keys() {
		owner, _ := settings.Owner(name)
		v.Owners[name] = owner
		if owner != settings.OwnerPlayer {
			continue
		}
		stored, ok := setting[any](name)
		if !ok {
			stored = settings.Unset(name)
		}
		v.Values[name] = stored
	}
	e, ok := engineSettings()
	if !ok {
		return v
	}
	v.ReadOnly[settings.OwnerEngine] = e.ReadOnly
	for engineName, value := range e.Values {
		if name, known := settings.FromEngine(engineName); known {
			v.Values[name] = value
		}
	}
	v.Pending = dotted(e.Pending)
	v.RestartKeys = dotted(e.RestartKeys)
	return v
}

func dotted(engineNames []string) []string {
	out := []string{}
	for _, n := range engineNames {
		if name, ok := settings.FromEngine(n); ok {
			out = append(out, name)
		}
	}
	return out
}

func getSettings(*call) sdk.Response {
	return jsonOK(http.StatusOK, buildSettingsView())
}

func invalidSetting(key, message string) sdk.Response {
	if key == "" {
		return fail(http.StatusBadRequest, "invalid_setting", message)
	}
	return fail(http.StatusBadRequest, "invalid_setting", message, "key", key)
}

// patchSettings saves the given keys all or nothing. Every key is checked
// before anything is written; the engine's part goes first, since it can
// refuse, and the player's part lands in one statement.
func patchSettings(c *call) sdk.Response {
	var patch map[string]any
	if err := decodeBody(c, &patch); err != nil || patch == nil {
		return invalidSetting("", "The body must be a JSON object of settings")
	}
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var engineKeys, playerKeys []string
	for _, k := range keys {
		if err := settings.Validate(k, patch[k]); err != nil {
			return invalidSetting(k, err.Error())
		}
		if owner, _ := settings.Owner(k); owner == settings.OwnerEngine {
			engineKeys = append(engineKeys, k)
		} else {
			playerKeys = append(playerKeys, k)
		}
	}
	if len(engineKeys) > 0 {
		if refused := patchEngine(patch, engineKeys); refused != nil {
			return *refused
		}
	}
	if len(playerKeys) > 0 {
		if err := storePlayerSettings(patch, playerKeys); err != nil {
			return internalError("saving the player settings", err)
		}
	}
	return jsonOK(http.StatusOK, buildSettingsView())
}

func patchEngine(patch map[string]any, keys []string) *sdk.Response {
	body := make(map[string]any, len(keys))
	for _, k := range keys {
		body[settings.EngineName(k)] = patch[k]
	}
	status, raw, err := engineFetch(http.MethodPatch, "/v1/settings", body)
	if err != nil {
		resp := engineUnavailable()
		return &resp
	}
	var resp sdk.Response
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusConflict:
		resp = fail(
			http.StatusConflict, "read_only",
			"The engine's config is read-only", "owner", settings.OwnerEngine,
		)
	case http.StatusBadRequest:
		resp = invalidSetting(keys[0], engineMessage(raw))
	default:
		resp = engineError(status, raw)
	}
	return &resp
}

func engineMessage(raw []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Message == "" {
		return "The engine refused the value"
	}
	return e.Message
}

func storePlayerSettings(patch map[string]any, keys []string) error {
	marks := make([]string, len(keys))
	args := make([]any, 0, 2*len(keys))
	for i, k := range keys {
		raw, err := json.Marshal(patch[k])
		if err != nil {
			return err
		}
		marks[i] = "(?, ?)"
		args = append(args, k, string(raw))
	}
	return exec(
		`INSERT INTO settings(key, value) VALUES `+strings.Join(marks, ", ")+
			` ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		args...,
	)
}
