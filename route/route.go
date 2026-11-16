// Package route is the method-and-path matcher behind the extension's route
// table. It knows nothing of the SDK, so it is tested natively.
package route

import (
	"net/http"
	"strings"
)

type entry[H any] struct {
	method   string
	segments []string
	handler  H
}

// Table holds handlers by method and pattern. The zero value is ready.
type Table[H any] struct {
	entries []entry[H]
}

// Add registers h for method and pattern. A pattern segment written {name}
// matches any one non-empty segment and binds it under name.
func (t *Table[H]) Add(method, pattern string, h H) {
	t.entries = append(t.entries, entry[H]{
		method:   method,
		segments: split(pattern),
		handler:  h,
	})
}

// Match finds the handler for method and path. The status is 200 on a hit,
// 405 when the path is known under other methods only, and 404 otherwise.
// Where several patterns fit, the one with the most literal segments wins.
func (t *Table[H]) Match(
	method, path string,
) (h H, params map[string]string, status int) {
	segs := split(path)
	best, bestLiterals := -1, -1
	pathKnown := false
	for i, e := range t.entries {
		literals, ok := fits(e.segments, segs)
		if !ok {
			continue
		}
		pathKnown = true
		if e.method == method && literals > bestLiterals {
			best, bestLiterals = i, literals
		}
	}
	if best < 0 {
		if pathKnown {
			return h, nil, http.StatusMethodNotAllowed
		}
		return h, nil, http.StatusNotFound
	}
	e := t.entries[best]
	params = map[string]string{}
	for i, s := range e.segments {
		if name, ok := param(s); ok {
			params[name] = segs[i]
		}
	}
	return e.handler, params, http.StatusOK
}

// split keeps a trailing empty segment, so "/a/" has two segments and never
// matches the pattern "/a".
func split(p string) []string {
	return strings.Split(strings.TrimPrefix(p, "/"), "/")
}

func param(segment string) (string, bool) {
	if len(segment) > 2 && segment[0] == '{' && segment[len(segment)-1] == '}' {
		return segment[1 : len(segment)-1], true
	}
	return "", false
}

func fits(pattern, path []string) (literals int, ok bool) {
	if len(pattern) != len(path) {
		return 0, false
	}
	for i, p := range pattern {
		if _, isParam := param(p); isParam {
			if path[i] == "" {
				return 0, false
			}
			continue
		}
		if p != path[i] {
			return 0, false
		}
		literals++
	}
	return literals, true
}
