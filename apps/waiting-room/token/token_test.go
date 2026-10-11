package token

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// Shared with the PHP side (apps/ticketing-api) through testdata/token-vector.txt.
const (
	vectorAdminToken = "test-admin-token"
	vectorPayload    = `{"q":"q_TEST","e":"00000000-0000-0000-0000-000000000001","exp":2000000000}`
	vectorToken      = "v1.eyJxIjoicV9URVNUIiwiZSI6IjAwMDAwMDAwLTAwMDAtMDAwMC0wMDAwLTAwMDAwMDAwMDAwMSIsImV4cCI6MjAwMDAwMDAwMH0.O_46wki893stPWYFzT9USGjfUkCqMnxjI0nldYZb8QI"
)

func TestFixedVector(t *testing.T) {
	s := NewSigner(vectorAdminToken)
	got := s.Sign(Payload{Q: "q_TEST", E: "00000000-0000-0000-0000-000000000001", Exp: 2000000000})
	t.Logf("payload %s", vectorPayload)
	t.Logf("token   %s", got)
	if got != vectorToken {
		t.Fatalf("token mismatch\n got  %s\n want %s", got, vectorToken)
	}
	payloadB64 := strings.Split(got, ".")[1]
	raw, err := b64.DecodeString(payloadB64)
	if err != nil || string(raw) != vectorPayload {
		t.Fatalf("payload JSON = %q, want %q", raw, vectorPayload)
	}
	file, err := os.ReadFile("../testdata/token-vector.txt")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(file)) != vectorToken {
		t.Fatalf("testdata/token-vector.txt is out of date: %q", file)
	}
	p, err := s.Verify(got, time.Unix(1999999999, 0))
	if err != nil || p.Q != "q_TEST" || p.Exp != 2000000000 {
		t.Fatalf("verify vector: %+v %v", p, err)
	}
}

func TestRoundTrip(t *testing.T) {
	s := NewSigner("secret")
	now := time.Unix(1_800_000_000, 0)
	tok := s.Sign(Payload{Q: "q_ABC", E: "e1", Exp: now.Unix() + 60})
	p, err := s.Verify(tok, now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Q != "q_ABC" || p.E != "e1" || p.Exp != now.Unix()+60 {
		t.Fatalf("payload %+v", p)
	}
	if strings.ContainsAny(tok, "=+/") {
		t.Fatalf("token must be unpadded url-safe: %s", tok)
	}
	// exp equal to now is still valid (expired only when exp < now).
	if _, err := s.Verify(tok, now.Add(60*time.Second)); err != nil {
		t.Fatalf("exp == now should be valid: %v", err)
	}
}

func TestExpired(t *testing.T) {
	s := NewSigner("secret")
	tok := s.Sign(Payload{Q: "q", E: "e", Exp: 100})
	if _, err := s.Verify(tok, time.Unix(101, 0)); !errors.Is(err, ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

func TestTamper(t *testing.T) {
	s := NewSigner("secret")
	now := time.Unix(1000, 0)
	tok := s.Sign(Payload{Q: "q_A", E: "e", Exp: 2000})
	parts := strings.Split(tok, ".")
	forged := s.Sign(Payload{Q: "q_B", E: "e", Exp: 2000})
	cases := map[string]string{
		"other key":       NewSigner("other").Sign(Payload{Q: "q_A", E: "e", Exp: 2000}),
		"swapped payload": "v1." + strings.Split(forged, ".")[1] + "." + parts[2],
		"flipped sig":     tok[:len(tok)-2] + flip(tok[len(tok)-2:]),
		"wrong version":   "v2." + parts[1] + "." + parts[2],
		"padded":          tok + "=",
		"extra part":      tok + ".x",
		"missing sig":     "v1." + parts[1],
		"empty":           "",
		"garbage":         "v1.%%%.%%%",
	}
	for name, bad := range cases {
		if _, err := s.Verify(bad, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func flip(s string) string {
	if s[0] == 'A' {
		return "B" + s[1:]
	}
	return "A" + s[1:]
}
