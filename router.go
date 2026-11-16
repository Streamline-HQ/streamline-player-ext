package main

import (
	"net/http"

	"github.com/datahearth/streamline/extensions/sdk"
	"github.com/streamline-hq/streamline-player-ext/access"
	routetable "github.com/streamline-hq/streamline-player-ext/route"
)

// Access says which credential a route takes; see package access.
type Access = access.Level

const (
	Public     = access.Public
	Any        = access.Any
	BearerOnly = access.BearerOnly
	DeviceOnly = access.DeviceOnly
	Admin      = access.Admin
)

type call struct {
	R      sdk.Request
	User   *User
	Device *Device
	Params map[string]string
}

type User struct {
	ID                uint32
	Name, Role, Email string
}

type Device struct {
	ID             int64
	Kind           string
	UserID         uint32
	Name, Platform string
	LastSeen       int64
}

type routed struct {
	access Access
	h      func(*call) sdk.Response
}

var routes routetable.Table[routed]

// handleRoute registers h for method and pattern, a path with {name} segments.
// The areas call it from their init.
func handleRoute(
	method, pattern string,
	access Access,
	h func(*call) sdk.Response,
) {
	routes.Add(method, pattern, routed{access: access, h: h})
}

func serve(r sdk.Request) sdk.Response {
	entry, params, status := routes.Match(r.Method, r.Path)
	switch status {
	case http.StatusNotFound:
		return fail(http.StatusNotFound, "not_found", "Not found")
	case http.StatusMethodNotAllowed:
		return fail(
			http.StatusMethodNotAllowed,
			"method_not_allowed",
			"Method not allowed",
		)
	}
	c := &call{R: r, Params: params}
	if entry.access != Public {
		if refused := authenticate(c, entry.access); refused != nil {
			return *refused
		}
	}
	return entry.h(c)
}
