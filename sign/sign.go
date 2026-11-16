// Package sign makes the signatures the engine checks on the URLs players
// fetch: HMAC-SHA256 over "{scope}|{id}|{exp}" with the shared secret, as
// engine/src/token.rs does.
package sign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
)

func Sign(secret, scope, id string, exp int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(scope + "|" + id + "|" + strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// EscapePath percent-encodes a file name the engine serves under a job and
// keeps its slashes.
func EscapePath(name string) string {
	return (&url.URL{Path: name}).EscapedPath()
}
