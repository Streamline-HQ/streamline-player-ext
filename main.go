// Command player is Streamline's player extension: the server behind every
// Streamline player app. It is a WebAssembly module the server loads, served
// at /extensions/player.
package main

import (
	"net/http"
	"strings"

	"github.com/datahearth/streamline/extensions/sdk"
)

const version = "0.2.1"

// main is never run in a reactor module. It names the two exports only so the
// unused linter sees them as entry points; nothing else calls them.
func main() { _, _ = manifest, handle }

//go:wasmexport manifest
func manifest() int32 {
	return sdk.WriteManifest(sdk.Manifest{
		Name:          "player",
		Version:       version,
		StreamlineAPI: sdk.APIVersion,
		Mounts: []sdk.Mount{
			{Prefix: "/", Auth: "self"},
			{Prefix: "/authorize", Auth: "session"},
		},
		Views: []string{
			"users", "user_logins", "movies", "series", "seasons", "episodes",
			"video_files", "people", "credits",
		},
		Folders:  []string{"movies", "series", "artwork", "cache"},
		Services: []string{"http"},
		Hooks:    []string{sdk.HookLibrary, sdk.HookSettings},
	})
}

//go:wasmexport handle
func handle() int32 { return sdk.Dispatch(route) }

// hooks and authorize are replaced from the init of the file that implements
// them, so adding one never edits this file.
var (
	hooks     = notFoundHandler
	authorize = notFoundHandler
)

func notFoundHandler(sdk.Request) sdk.Response {
	return fail(http.StatusNotFound, "not_found", "Not found")
}

func route(r sdk.Request) (resp sdk.Response) {
	defer func() {
		if p := recover(); p != nil {
			sdk.Log("error", "a handler panicked", "path", r.Path, "panic", p)
			resp = fail(http.StatusInternalServerError, "internal", "Internal error")
		}
	}()
	if err := ensureSchema(); err != nil {
		sdk.Log("error", "preparing the database failed", "error", err.Error())
		return fail(http.StatusInternalServerError, "internal", "Internal error")
	}
	fileSettings, fileSecrets = r.Settings, r.Secrets
	switch {
	case r.Mount == "" && strings.HasPrefix(r.Path, "/hooks/"):
		return hooks(r)
	case r.Mount == "/authorize":
		return authorize(r)
	case r.Mount == "/":
		sweep(r)
	}
	return serve(r)
}

// sweep runs at the start of every request on the self mount; the playback
// area replaces it from its init to stop the sessions that went silent.
var sweep = func(sdk.Request) {}
