// Package settings knows the server settings the player exposes under
// /admin/settings: which keys exist, who owns them and what values they take.
// It knows nothing of the SDK, so it is tested natively.
package settings

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
)

const (
	OwnerEngine = "engine"
	OwnerPlayer = "player"

	maxText    = 300
	maxEntries = 64
)

type kind uint8

const (
	choice kind = iota
	text
	boolean
	number
	baseURL
	origins
	appLinks
	cidrs
)

type key struct {
	name   string
	owner  string
	engine string
	kind   kind
}

var table = []key{
	{"transcoding.hw_accel", OwnerEngine, "hw_accel", choice},
	{"transcoding.hw_device", OwnerEngine, "hw_device", text},
	{"stream.tonemap", OwnerEngine, "tonemap", boolean},
	{"stream.max_live", OwnerEngine, "max_transcodes", number},
	{"engine.url", OwnerPlayer, "", baseURL},
	{"engine.public_url", OwnerPlayer, "", baseURL},
	{"player.url", OwnerPlayer, "", baseURL},
	{"player.origins", OwnerPlayer, "", origins},
	{"player.app_links", OwnerPlayer, "", appLinks},
	{"network.home", OwnerPlayer, "", cidrs},
}

var hwAccel = []string{"auto", "none", "vaapi"}

// ErrUnknown is what Validate says for a key that is not a setting.
var ErrUnknown = errors.New("unknown setting")

func find(name string) (key, bool) {
	for _, k := range table {
		if k.name == name {
			return k, true
		}
	}
	return key{}, false
}

// Keys lists every setting, the engine's first.
func Keys() []string {
	out := make([]string, len(table))
	for i, k := range table {
		out[i] = k.name
	}
	return out
}

// Owner is OwnerEngine or OwnerPlayer; ok is false for an unknown key.
func Owner(name string) (owner string, ok bool) {
	k, ok := find(name)
	return k.owner, ok
}

// EngineName is the key's name in the engine's own settings, empty for a key
// the engine does not own.
func EngineName(name string) string {
	k, _ := find(name)
	return k.engine
}

// FromEngine is the setting an engine key name belongs to.
func FromEngine(engineName string) (string, bool) {
	for _, k := range table {
		if k.engine != "" && k.engine == engineName {
			return k.name, true
		}
	}
	return "", false
}

// Unset is what an unset player key reads as; nil for the engine's keys.
func Unset(name string) any {
	k, _ := find(name)
	switch k.kind {
	case baseURL:
		return ""
	case origins, appLinks, cidrs:
		return []string{}
	}
	return nil
}

// Validate says why v, a decoded JSON value, is not acceptable for the key.
func Validate(name string, v any) error {
	k, ok := find(name)
	if !ok {
		return ErrUnknown
	}
	switch k.kind {
	case choice:
		s, ok := v.(string)
		if !ok || !slices.Contains(hwAccel, s) {
			return fmt.Errorf("must be one of %v", hwAccel)
		}
	case text:
		s, ok := v.(string)
		if !ok || s == "" || len(s) > 200 {
			return errors.New("must be a string of 1 to 200 characters")
		}
	case boolean:
		if _, ok := v.(bool); !ok {
			return errors.New("must be true or false")
		}
	case number:
		n, ok := integer(v)
		if !ok || n < 1 || n > 8 {
			return errors.New("must be a whole number from 1 to 8")
		}
	case baseURL:
		return validURL(v)
	case origins:
		return each(v, validOrigin)
	case appLinks:
		return each(v, validAppLink)
	case cidrs:
		return each(v, validCIDR)
	}
	return nil
}

func integer(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true
		}
	}
	return 0, false
}

// validURL takes an absolute http or https URL without a query, or "" to
// clear it.
func validURL(v any) error {
	s, ok := v.(string)
	if !ok {
		return errors.New("must be a string")
	}
	if s == "" {
		return nil
	}
	u, err := parseAbsolute(s)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("must be an http or https URL")
	}
	if u.Host == "" {
		return errors.New("must name a host")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("must not have a query or fragment")
	}
	return nil
}

func parseAbsolute(s string) (*url.URL, error) {
	if len(s) > maxText {
		return nil, errors.New("is too long")
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.User != nil {
		return nil, errors.New("must be an absolute URL")
	}
	return u, nil
}

func each(v any, check func(string) error) error {
	var list []string
	switch l := v.(type) {
	case []string:
		list = l
	case []any:
		for _, e := range l {
			s, ok := e.(string)
			if !ok {
				return errors.New("must be a list of strings")
			}
			list = append(list, s)
		}
	default:
		return errors.New("must be a list of strings")
	}
	if len(list) > maxEntries {
		return fmt.Errorf("takes at most %d entries", maxEntries)
	}
	for _, s := range list {
		if err := check(s); err != nil {
			return fmt.Errorf("%q %w", s, err)
		}
	}
	return nil
}

func validOrigin(s string) error {
	u, err := parseAbsolute(s)
	if err != nil {
		return err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		s != u.Scheme+"://"+u.Host {
		return errors.New("must be exactly scheme://host[:port]")
	}
	return nil
}

func validAppLink(s string) error {
	u, err := parseAbsolute(s)
	if err != nil {
		return err
	}
	if (u.Host == "" && u.Opaque == "") || u.Fragment != "" {
		return errors.New("must be an absolute link without a fragment")
	}
	return nil
}

func validCIDR(s string) error {
	if _, err := netip.ParsePrefix(s); err != nil {
		return errors.New("must be a CIDR such as 192.168.1.0/24")
	}
	return nil
}
