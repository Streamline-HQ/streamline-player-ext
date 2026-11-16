// Package access decides whether a credential may call a route.
package access

import "net/http"

// Level says which credential a route takes. A route declares it when it is
// registered, so a route nobody declared for a device key is never served to
// one.
type Level uint8

const (
	Public Level = iota
	// Any takes a device key or a browser's access token.
	Any
	BearerOnly
	DeviceOnly
	// Admin takes either credential, from an admin.
	Admin
)

// Cred is the credential a request presented.
type Cred uint8

const (
	None Cred = iota
	Key
	Bearer
)

// Decide answers 200 and an empty code when the credential may call a route
// at level, otherwise the status and error code to refuse with. It fails
// closed: a level it does not know refuses everything.
func Decide(level Level, cred Cred, role string) (status int, code string) {
	if level == Public {
		return http.StatusOK, ""
	}
	if cred == None {
		return http.StatusUnauthorized, "unauthorized"
	}
	switch level {
	case Any:
	case BearerOnly:
		if cred == Key {
			return http.StatusForbidden, "session_required"
		}
	case DeviceOnly:
		if cred == Bearer {
			return http.StatusForbidden, "forbidden"
		}
	case Admin:
		if role != "admin" {
			return http.StatusForbidden, "forbidden"
		}
	default:
		return http.StatusForbidden, "forbidden"
	}
	return http.StatusOK, ""
}
