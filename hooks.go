package main

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/datahearth/streamline/extensions/sdk"
)

func init() { hooks = libraryHook }

// libraryHook is what Streamline calls after an import. Only the host reaches
// it, so it checks no credential.
func libraryHook(r sdk.Request) sdk.Response {
	if r.Path != "/hooks/library" || r.Method != http.MethodPost {
		return notFoundHandler(r)
	}
	var body scanScope
	if json.Unmarshal(r.Body, &body) != nil {
		return fail(http.StatusBadRequest, "invalid_request", "Invalid request")
	}
	body.Full = slices.DeleteFunc(body.Full, func(f string) bool {
		return f != "movies" && f != "series"
	})
	if body.empty() {
		return fail(http.StatusBadRequest, "invalid_request", "Nothing to compare")
	}
	rememberFolders(r)
	done, err := compare(body, compareBudget)
	if err != nil {
		return libraryFailed(err)
	}
	noteUnfinished(done)
	return jsonOK(http.StatusAccepted, struct{}{})
}
