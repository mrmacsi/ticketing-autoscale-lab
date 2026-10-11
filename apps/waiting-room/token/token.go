// Package token signs and verifies waiting-room admission tokens (docs/waiting-room.md section 3.2):
//
//	token      = "v1." + b64url(payloadJSON) + "." + b64url(HMAC_SHA256(signingKey, "v1." + b64url(payloadJSON)))
//	payload    = {"q": "<queueId>", "e": "<eventId>", "exp": <unix seconds>}
//	signingKey = HMAC_SHA256(key = LAB_ADMIN_TOKEN, message = "waiting-room-token-v1")   (raw 32 bytes)
//
// b64url is the URL-safe base64 alphabet without padding. Verification is stateless.
package token

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	prefix     = "v1."
	keyContext = "waiting-room-token-v1"
	maxLen     = 2048
)

var (
	// ErrInvalid is returned for a malformed token or a bad signature.
	ErrInvalid = errors.New("queue_token_invalid")
	// ErrExpired is returned for a correctly signed token whose exp is in the past.
	ErrExpired = errors.New("queue_token_expired")
)

var b64 = base64.RawURLEncoding

// Payload is serialized with keys in the order q, e, exp.
type Payload struct {
	Q   string `json:"q"`
	E   string `json:"e"`
	Exp int64  `json:"exp"`
}

// Signer holds the derived signing key.
type Signer struct {
	key []byte
}

// NewSigner derives the signing key from the lab admin token.
func NewSigner(adminToken string) *Signer {
	m := hmac.New(sha256.New, []byte(adminToken))
	m.Write([]byte(keyContext))
	return &Signer{key: m.Sum(nil)}
}

// Sign returns "v1.<payload>.<sig>".
func (s *Signer) Sign(p Payload) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // match PHP json_encode(..., JSON_UNESCAPED_SLASHES) for ASCII ids
	_ = enc.Encode(p)        // cannot fail for strings and an int64
	signed := prefix + b64.EncodeToString(bytes.TrimRight(buf.Bytes(), "\n"))
	return signed + "." + b64.EncodeToString(s.mac(signed))
}

// Verify checks format and signature, then expiry (expired when exp < now in unix seconds).
func (s *Signer) Verify(tok string, now time.Time) (Payload, error) {
	if len(tok) > maxLen || !strings.HasPrefix(tok, prefix) {
		return Payload{}, ErrInvalid
	}
	payloadB64, sigB64, ok := strings.Cut(tok[len(prefix):], ".")
	if !ok || payloadB64 == "" || sigB64 == "" || strings.Contains(sigB64, ".") {
		return Payload{}, ErrInvalid
	}
	sig, err := b64.Strict().DecodeString(sigB64)
	if err != nil || !hmac.Equal(sig, s.mac(prefix+payloadB64)) {
		return Payload{}, ErrInvalid
	}
	raw, err := b64.Strict().DecodeString(payloadB64)
	if err != nil {
		return Payload{}, ErrInvalid
	}
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil || p.Q == "" || p.E == "" || p.Exp == 0 {
		return Payload{}, ErrInvalid
	}
	if p.Exp < now.Unix() {
		return p, ErrExpired
	}
	return p, nil
}

func (s *Signer) mac(msg string) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}
