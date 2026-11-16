package harness

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Tokens is what a browser holds after signing in.
type Tokens struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
	DeviceID         string `json:"device_id"`
}

// LoopbackRedirect is a redirect_uri an app may use on any stack.
const LoopbackRedirect = "http://127.0.0.1:41999/cb"

// PKCE returns a random verifier and its S256 challenge.
func PKCE() (verifier, challenge string) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Errorf("harness: %w", err))
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

var noFollow = &http.Client{
	Timeout: client.Timeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// session returns u's Streamline session cookie, signing in once per user.
func (s *Stack) session(u User) (string, error) {
	if c, ok := s.sessions[u.Email]; ok {
		return c, nil
	}
	c, err := s.Login(u.Email, u.Password)
	if err != nil {
		return "", err
	}
	if s.sessions == nil {
		s.sessions = map[string]string{}
	}
	s.sessions[u.Email] = c
	return c, nil
}

// Authorize opens the extension's /authorize as u, signed in to Streamline,
// and returns the answer without following the redirect.
func (s *Stack) Authorize(
	u User,
	q url.Values,
) (status int, location string, body []byte, err error) {
	cookie, err := s.session(u)
	if err != nil {
		return 0, "", nil, err
	}
	req, err := http.NewRequest(
		http.MethodGet, s.BaseURL+MountBase+"/authorize?"+q.Encode(), nil,
	)
	if err != nil {
		return 0, "", nil, err
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	resp, err := noFollow.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", nil, err
	}
	return resp.StatusCode, resp.Header.Get("Location"), raw, nil
}

// AuthorizeQuery is the query /authorize takes for an S256 sign-in.
func AuthorizeQuery(redirectURI, state, challenge string) url.Values {
	return url.Values{
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
}

// PostToken calls POST /auth/token.
func (s *Stack) PostToken(body any) (status int, raw []byte, err error) {
	return s.Do(http.MethodPost, MountBase+"/auth/token", "", body)
}

// code runs /authorize for u and returns the code it redirected back with.
func (s *Stack) code(u User, redirectURI, challenge string) (string, error) {
	status, loc, raw, err := s.Authorize(
		u, AuthorizeQuery(redirectURI, "state", challenge),
	)
	if err != nil {
		return "", err
	}
	if status != http.StatusFound {
		return "", fmt.Errorf("authorize: %d %s", status, raw)
	}
	target, err := url.Parse(loc)
	if err != nil {
		return "", err
	}
	code := target.Query().Get("code")
	if code == "" {
		return "", fmt.Errorf("authorize redirected without a code: %s", loc)
	}
	return code, nil
}

// SignInBrowser signs u in as a web player at origin, which must be on the
// extension's origin list, and returns the browser's tokens.
func (s *Stack) SignInBrowser(u User, origin string) Tokens {
	redirect := origin + "/cb"
	verifier, challenge := PKCE()
	code, err := s.code(u, redirect, challenge)
	if err != nil {
		panic(fmt.Errorf("harness: %w", err))
	}
	status, raw, err := s.PostToken(map[string]string{
		"grant_type": "authorization_code", "code": code,
		"code_verifier": verifier, "redirect_uri": redirect,
		"device_name": "Harness browser",
	})
	if err != nil {
		panic(fmt.Errorf("harness: browser token: %w", err))
	}
	if status != http.StatusOK {
		panic(fmt.Errorf("harness: browser token: %d %s", status, raw))
	}
	var t Tokens
	if err := json.Unmarshal(raw, &t); err != nil {
		panic(fmt.Errorf("harness: %w", err))
	}
	return t
}

// SignInApp signs u in as the native app clientID and returns its device key.
func (s *Stack) SignInApp(u User, clientID string) (apiKey string) {
	k, err := s.signInApp(u, clientID)
	if err != nil {
		panic(fmt.Errorf("harness: %w", err))
	}
	return k
}

func (s *Stack) signInApp(u User, clientID string) (string, error) {
	verifier, challenge := PKCE()
	code, err := s.code(u, LoopbackRedirect, challenge)
	if err != nil {
		return "", err
	}
	status, raw, err := s.PostToken(map[string]string{
		"grant_type": "authorization_code", "code": code,
		"code_verifier": verifier, "redirect_uri": LoopbackRedirect,
		"client_id": clientID, "name": "Harness app", "platform": "linux",
	})
	if err != nil {
		return "", fmt.Errorf("app token: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("app token: %d %s", status, raw)
	}
	var k struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(raw, &k); err != nil {
		return "", err
	}
	return k.APIKey, nil
}

// SetSetting stores a player setting the way the extension reads it.
func (s *Stack) SetSetting(key string, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Errorf("harness: %w", err))
	}
	db := s.Ext()
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, string(raw),
	); err != nil {
		panic(fmt.Errorf("harness: %w", err))
	}
}

// Call sends one request to the extension with the given headers; a non-nil
// body is sent as JSON.
func (s *Stack) Call(
	method, path string,
	headers map[string]string,
	body any,
) (status int, raw []byte, err error) {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		payload = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.BaseURL+MountBase+path, payload)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err = io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}
