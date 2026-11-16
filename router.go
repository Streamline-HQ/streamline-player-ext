package main

import (
	"errors"
	"net/http"

	"github.com/datahearth/streamline/extensions/sdk"
	routetable "github.com/streamline-hq/streamline-player-ext/route"
)

// Access says which credential a route takes. A route declares it when it is
// registered, so a route nobody declared for a device key is never served to
// one.
type Access uint8

const (
	Public Access = iota
	Any
	BearerOnly
	DeviceOnly
	Admin
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
}

type routed struct {
	access Access
	h      func(*call) sdk.Response
}

var routes routetable.Table[routed]

// handleRoute registers h for method and pattern, a path with {name} segments.
// The areas call it from their init.
//
//nolint:unused // the areas register their routes after the skeleton
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
		user, device, err := authenticate(r, entry.access)
		if err != nil {
			return fail(http.StatusUnauthorized, "unauthorized", "Sign in required")
		}
		c.User, c.Device = user, device
	}
	return entry.h(c)
}

var errUnauthenticated = errors.New("not authenticated")

// authenticate refuses every credential until the credentials area replaces it.
func authenticate(_ sdk.Request, _ Access) (*User, *Device, error) {
	return nil, nil, errUnauthenticated
}
