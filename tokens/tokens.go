// Package tokens makes and checks the secrets the extension hands out.
package tokens

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"strings"
)

const (
	codeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
	codeLength   = 6
)

// New returns 32 random bytes as unpadded base64url.
func New() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Hash is the SHA-256 hex a secret is stored as.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Sign is HMAC-SHA256 of msg as lowercase hex.
func Sign(secret, msg string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

// PKCEMatches checks an S256 challenge; the plain method is not supported.
func PKCEMatches(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(want), []byte(challenge)) == 1
}

// UserCode returns a pairing code of six letters a person can read out.
func UserCode() string {
	n := big.NewInt(int64(len(codeAlphabet)))
	out := make([]byte, codeLength)
	for i := range out {
		v, err := rand.Int(rand.Reader, n)
		if err != nil {
			panic(err)
		}
		out[i] = codeAlphabet[v.Int64()]
	}
	return string(out)
}

// NormalizeUserCode accepts a code typed with or without its dash, in either
// case.
func NormalizeUserCode(in string) (string, bool) {
	code := strings.ToUpper(strings.Replace(in, "-", "", 1))
	if len(code) != codeLength || strings.Trim(code, codeAlphabet) != "" {
		return "", false
	}
	return code, true
}
