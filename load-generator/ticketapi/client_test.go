package ticketapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/metrics"
)

func TestParseBaseURL(t *testing.T) {
	for _, bad := range []string{"ftp://x", "http://", "http://u:p@host", "http://host/?q=1", "not a url"} {
		if _, err := ParseBaseURL(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	u, err := ParseBaseURL("http://34.1.2.3/prefix/")
	if err != nil || u.JoinPath("api", "events").String() != "http://34.1.2.3/prefix/api/events" {
		t.Fatalf("%v %v", u, err)
	}
}

func TestClientStaysOnTarget(t *testing.T) {
	var other atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { other.Add(1) }))
	defer elsewhere.Close()
	var paths []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.EscapedPath())
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	defer target.Close()

	c, err := NewClient(target.URL, NewHTTPClient(2, time.Second), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := c.GetEvent(context.Background(), "abc")
	if r.Status != http.StatusFound || other.Load() != 0 {
		t.Fatalf("redirect followed: status=%d other=%d", r.Status, other.Load())
	}
	for _, seg := range []string{"..", "a/b", "x%2F", "?q", ""} {
		if r := c.GetEvent(context.Background(), seg); r.Err == nil {
			t.Errorf("segment %q must be refused", seg)
		}
	}
	if len(paths) != 1 || paths[0] != "/api/events/abc" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestTimeoutClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL, NewHTTPClient(2, 5*time.Second), 100*time.Millisecond, nil)
	r := c.ListEvents(context.Background())
	if r.Class != metrics.ClassTimeout || !r.Unknown() || r.Aborted {
		t.Fatalf("response = %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if r := c.ListEvents(ctx); !r.Aborted || r.Unknown() {
		t.Fatalf("cancelled request must be aborted: %+v", r)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	c2, _ := NewClient(url, NewHTTPClient(2, time.Second), time.Second, nil)
	if r := c2.ListEvents(context.Background()); r.Class != metrics.ClassNetworkError {
		t.Fatalf("closed port = %+v", r)
	}
}
