// Package gcp holds the two Google Cloud calls the waiting room needs without the Cloud SDK: an
// access token from the metadata server and a Secret Manager secret version read.
package gcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// TokenSource returns an OAuth access token.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken is a fixed token (tests, local use).
type StaticToken string

func (s StaticToken) Token(context.Context) (string, error) { return string(s), nil }

// MetadataTokens fetches and caches the default service account token from the metadata server.
type MetadataTokens struct {
	BaseURL string // default http://$GCE_METADATA_HOST or http://metadata.google.internal
	Client  *http.Client

	mu  sync.Mutex
	tok string
	exp time.Time
}

// NewMetadataTokens uses GCE_METADATA_HOST when set.
func NewMetadataTokens() *MetadataTokens {
	host := os.Getenv("GCE_METADATA_HOST")
	if host == "" {
		host = "metadata.google.internal"
	}
	return &MetadataTokens{BaseURL: "http://" + host, Client: &http.Client{Timeout: 5 * time.Second}}
}

func (m *MetadataTokens) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tok != "" && time.Now().Before(m.exp) {
		return m.tok, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		m.BaseURL+"/computeMetadata/v1/instance/service-accounts/default/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	body, err := Do(m.Client, req)
	if err != nil {
		return "", fmt.Errorf("metadata token: %w", err)
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" {
		return "", errors.New("metadata token: malformed response")
	}
	m.tok = t.AccessToken
	// Refresh a minute early (metadata tokens normally live ~1 h).
	m.exp = time.Now().Add(time.Duration(max(t.ExpiresIn-60, 10)) * time.Second)
	return m.tok, nil
}

// AccessSecret reads the latest version of a secret ("projects/N/secrets/X", or a full version
// name) and returns its value with trailing newlines removed, which matches the ticketing API's
// entrypoint (shell command substitution strips them) so both sides derive the same signing key.
func AccessSecret(ctx context.Context, client *http.Client, ts TokenSource, endpoint, name string) (string, error) {
	if endpoint == "" {
		endpoint = "https://secretmanager.googleapis.com/v1"
	}
	name = strings.Trim(name, "/")
	if !strings.Contains(name, "/versions/") {
		name += "/versions/latest"
	}
	tok, err := ts.Token(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/"+name+":access", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	body, err := Do(client, req)
	if err != nil {
		return "", fmt.Errorf("secret %s: %w", name, err)
	}
	var v struct {
		Payload struct {
			Data string `json:"data"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", fmt.Errorf("secret %s: malformed response", name)
	}
	raw, err := base64.StdEncoding.DecodeString(v.Payload.Data)
	if err != nil {
		return "", fmt.Errorf("secret %s: bad payload encoding", name)
	}
	val := strings.TrimRight(string(raw), "\r\n")
	if val == "" {
		return "", fmt.Errorf("secret %s is empty", name)
	}
	return val, nil
}

// Do performs req and returns the body of a 200 response, or an error with a short body excerpt.
func Do(client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, Excerpt(body))
	}
	return body, nil
}

// Excerpt shortens a response body for error messages.
func Excerpt(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
