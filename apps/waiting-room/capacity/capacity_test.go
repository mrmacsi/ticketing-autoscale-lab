package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/gcp"
	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/queue"
)

const group = "https://www.googleapis.com/compute/v1/projects/p1/zones/europe-west2-b/instanceGroups/app-mig"

func TestGCEGetHealth(t *testing.T) {
	sample, err := os.ReadFile("testdata/gethealth.json")
	if err != nil {
		t.Fatal(err)
	}
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" || r.URL.Path != "/computeMetadata/v1/instance/service-accounts/default/token" {
			http.Error(w, "bad metadata request", 403)
			return
		}
		io.WriteString(w, `{"access_token":"tok-123","expires_in":3599,"token_type":"Bearer"}`)
	}))
	defer meta.Close()
	compute := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Method != "POST" || r.URL.Path != "/projects/p1/global/backendServices/app-bs/getHealth" ||
			r.Header.Get("Authorization") != "Bearer tok-123" || body["group"] != group {
			http.Error(w, `{"error":{"code":403}}`, 403)
			return
		}
		w.Write(sample)
	}))
	defer compute.Close()

	tokens := &gcp.MetadataTokens{BaseURL: meta.URL, Client: meta.Client()}
	g := &GCE{Project: "p1", BackendService: "app-bs", Group: group, AppPort: 8080, Endpoint: compute.URL, Tokens: tokens, Client: compute.Client()}
	servers, err := g.Healthy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []queue.Server{
		{Name: "ticketing-scale-lab-app-0f9e", IP: "10.60.0.7", BaseURL: "http://10.60.0.7:8080"},
		{Name: "ticketing-scale-lab-app-ab12", IP: "10.60.0.9", BaseURL: "http://10.60.0.9:8080"},
	}
	if len(servers) != 2 || servers[0] != want[0] || servers[1] != want[1] {
		t.Fatalf("servers %+v", servers)
	}

	g.BackendService = "other"
	if _, err := g.Healthy(context.Background()); err == nil {
		t.Fatal("expected error for HTTP 403")
	}
}

// Recorded from the lab's real backend service during preset F (3 healthy VMs), so the parser is
// checked against Google's actual response shape without calling the API.
func TestParseRecordedGetHealth(t *testing.T) {
	body, err := os.ReadFile("testdata/gethealth-gcp-3-servers.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseGetHealth(body, 8080)
	if err != nil || len(s) != 3 {
		t.Fatalf("%+v %v", s, err)
	}
	if s[0].Name != "ticketing-scale-lab-app-76td" || s[0].BaseURL != "http://10.60.0.24:8080" {
		t.Fatalf("first server %+v", s[0])
	}
}

func TestParseGetHealthEmptyGroup(t *testing.T) {
	s, err := ParseGetHealth([]byte(`{"kind":"compute#backendServiceGroupHealth"}`), 8080)
	if err != nil || len(s) != 0 {
		t.Fatalf("%v %v", s, err)
	}
	if _, err := ParseGetHealth([]byte(`<html>`), 8080); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestStatic(t *testing.T) {
	var up atomic.Bool
	up.Store(true)
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" && up.Load() {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(503)
	}))
	defer a.Close()
	urls, err := ParseStaticBackends(" " + a.URL + "/ , http://127.0.0.1:1 ")
	if err != nil || len(urls) != 2 || urls[0] != a.URL {
		t.Fatalf("%v %v", urls, err)
	}
	s := &Static{BaseURLs: urls, Client: &http.Client{Timeout: time.Second}}
	got, err := s.Healthy(context.Background())
	if err != nil || len(got) != 1 || got[0].BaseURL != a.URL || got[0].IP != "127.0.0.1" {
		t.Fatalf("%+v %v", got, err)
	}
	up.Store(false)
	if got, _ := s.Healthy(context.Background()); len(got) != 0 {
		t.Fatalf("503 counted healthy: %+v", got)
	}
	if _, err := ParseStaticBackends("ftp://x"); err == nil {
		t.Fatal("expected error")
	}
}

type fakeSource struct {
	servers []queue.Server
	err     error
}

func (f *fakeSource) Name() string { return "fake" }
func (f *fakeSource) Healthy(context.Context) ([]queue.Server, error) {
	return f.servers, f.err
}

func TestMonitorKeepsLastKnownFor30s(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	src := &fakeSource{servers: []queue.Server{{Name: "a"}, {Name: "b"}}}
	var last queue.CapacityUpdate
	m := &Monitor{Source: src, Apply: func(u queue.CapacityUpdate) { last = u }, Interval: time.Second, Now: func() time.Time { return now }}
	m.Poll(context.Background())
	if last.Healthy != 2 || last.Err != "" || last.Source != "fake" {
		t.Fatalf("%+v", last)
	}
	src.err = errors.New("compute API down")
	now = now.Add(30 * time.Second)
	m.Poll(context.Background())
	if last.Healthy != 2 || last.Err != "compute API down" {
		t.Fatalf("within 30 s: %+v", last)
	}
	now = now.Add(time.Second)
	m.Poll(context.Background())
	if last.Healthy != 0 || len(last.Servers) != 0 || last.Err == "" {
		t.Fatalf("after 30 s: %+v", last)
	}
	src.err = nil
	m.Poll(context.Background())
	if last.Healthy != 2 || last.Err != "" {
		t.Fatalf("recovered: %+v", last)
	}

	// Never succeeded: zero immediately.
	m2 := &Monitor{Source: &fakeSource{err: errors.New("x")}, Apply: func(u queue.CapacityUpdate) { last = u }, Interval: time.Second}
	m2.Poll(context.Background())
	if last.Healthy != 0 || last.Err != "x" {
		t.Fatalf("%+v", last)
	}
}
