package main

import (
	"net/http"

	"github.com/datahearth/streamline/extensions/sdk"
)

func init() {
	handleRoute(http.MethodGet, "/me", Any, getMe)
}

func getMe(c *call) sdk.Response {
	return jsonOK(http.StatusOK, map[string]string{
		"name": c.User.Name, "email": c.User.Email, "role": c.User.Role,
	})
}
