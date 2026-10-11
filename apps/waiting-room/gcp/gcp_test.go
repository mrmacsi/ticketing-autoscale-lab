package gcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAccessSecretAndTokenCache(t *testing.T) {
	var tokenCalls atomic.Int32
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			w.WriteHeader(403)
			return
		}
		tokenCalls.Add(1)
		io.WriteString(w, `{"access_token":"tok","expires_in":3599}`)
	}))
	defer meta.Close()
	sm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/projects/123/secrets/admin-token/versions/latest:access" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":404,"message":"not found"}}`)
			return
		}
		// base64("s3cret\n")
		io.WriteString(w, `{"name":"x","payload":{"data":"czNjcmV0Cg==","dataCrc32c":"1"}}`)
	}))
	defer sm.Close()

	ts := &MetadataTokens{BaseURL: meta.URL, Client: meta.Client()}
	for range 2 {
		v, err := AccessSecret(context.Background(), sm.Client(), ts, sm.URL, "projects/123/secrets/admin-token")
		if err != nil || v != "s3cret" {
			t.Fatalf("%q %v", v, err)
		}
	}
	if tokenCalls.Load() != 1 {
		t.Fatalf("token fetched %d times, want 1 (cached)", tokenCalls.Load())
	}
	if _, err := AccessSecret(context.Background(), sm.Client(), ts, sm.URL, "projects/123/secrets/other"); err == nil {
		t.Fatal("expected error for 404")
	}
}
