// Package redirect decides whether a sign-in may return to a redirect_uri.
package redirect

import (
	"net/url"
	"strings"
)

// Kind says which allowance let a redirect_uri through.
type Kind uint8

const (
	Refused Kind = iota
	// Web is an origin on the web player list.
	Web
	// Loopback is http://127.0.0.1:<port>, which a native app opens for the
	// browser to come back to (RFC 8252).
	Loopback
	// App is a link an app registered with the system.
	App
)

// Classify says what redirect_uri is. A web origin on the list wins over the
// loopback rule, so a development player on 127.0.0.1 stays a web player.
func Classify(uri string, origins, appLinks []string) Kind {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme == "" || u.Fragment != "" || u.User != nil ||
		(u.Host == "" && u.Opaque == "") {
		return Refused
	}
	origin := u.Scheme + "://" + u.Host
	for _, o := range origins {
		if strings.EqualFold(o, origin) {
			return Web
		}
	}
	if u.Scheme == "http" && u.Hostname() == "127.0.0.1" && u.Port() != "" {
		return Loopback
	}
	for _, l := range appLinks {
		if underLink(u, l) {
			return App
		}
	}
	return Refused
}

func underLink(u *url.URL, link string) bool {
	l, err := url.Parse(link)
	if err != nil || l.Scheme == "" {
		return false
	}
	if !strings.EqualFold(u.Scheme, l.Scheme) || !strings.EqualFold(u.Host, l.Host) {
		return false
	}
	prefix := strings.TrimSuffix(l.Path, "/")
	return u.Path == l.Path || u.Path == prefix ||
		strings.HasPrefix(u.Path, prefix+"/")
}
