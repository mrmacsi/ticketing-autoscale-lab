package queueapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type rec struct {
	mu      sync.Mutex
	ok, bad int
}

func (r *rec) RecordQueueRequest(ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ok {
		r.ok++
	} else {
		r.bad++
	}
}

func TestClientRequestsAndDecoding(t *testing.T) {
	var gotJoin, gotRelease map[string]string
	var gotWait string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /lab/queue/join", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotJoin)
		_, _ = w.Write([]byte(`{"data":{"queueId":"q_abc","eventId":"e1","status":"waiting","position":12,"waiting":40,"estimatedWaitSeconds":null,"pollAfterMs":0}}`))
	})
	mux.HandleFunc("GET /lab/queue/status/{id}", func(w http.ResponseWriter, r *http.Request) {
		gotWait = r.URL.Query().Get("waitSeconds")
		_, _ = w.Write([]byte(`{"data":{"queueId":"` + r.PathValue("id") + `","eventId":"e1","status":"admitted","position":0,"waiting":3,"estimatedWaitSeconds":4.5,"token":"v1.a.b","tokenExpiresAt":"2026-10-10T15:00:00.000Z","pollAfterMs":0}}`))
	})
	mux.HandleFunc("POST /lab/queue/release", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotRelease)
		_, _ = w.Write([]byte(`{"data":{"released":true}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	r := &rec{}
	c, err := NewClient(srv.URL+"/lab/", NewHTTPClient(10), r)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := c.Join(ctx, "e1", "cust-1-2")
	if err != nil || st.Status != StatusWaiting || st.Position != 12 || st.Waiting != 40 || st.EstimatedWaitSeconds != nil {
		t.Fatalf("join = %+v %v", st, err)
	}
	if gotJoin["eventId"] != "e1" || gotJoin["customerRef"] != "cust-1-2" {
		t.Fatalf("join body = %v", gotJoin)
	}
	st, err = c.Status(ctx, "q_abc", 99)
	if err != nil || st.Status != StatusAdmitted || st.Token != "v1.a.b" || *st.EstimatedWaitSeconds != 4.5 || gotWait != "25" {
		t.Fatalf("status = %+v %v wait=%s", st, err, gotWait)
	}
	if err := c.Release(ctx, "q_abc", ReleasePurchased); err != nil || gotRelease["queueId"] != "q_abc" || gotRelease["outcome"] != "purchased" {
		t.Fatalf("release = %v %v", err, gotRelease)
	}
	if r.ok != 3 || r.bad != 0 {
		t.Fatalf("recorded ok=%d bad=%d", r.ok, r.bad)
	}
	if _, err := c.Status(ctx, "../admin", 0); err == nil || IsRetryable(err) {
		t.Fatalf("unsafe id must be refused without a request: %v", err)
	}
	if r.ok+r.bad != 3 {
		t.Fatal("refused request must not be recorded")
	}
}

func TestErrorClassification(t *testing.T) {
	codes := map[string]int{"/queue/join": 503}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue/join":
			w.WriteHeader(codes["/queue/join"])
			_, _ = w.Write([]byte(`{"error":"x","message":"y"}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"unknown_queue_id","message":"gone"}`))
		}
	}))
	defer srv.Close()
	r := &rec{}
	c, _ := NewClient(srv.URL, NewHTTPClient(10), r)
	ctx := context.Background()
	for code, retry := range map[int]bool{503: true, 500: true, 429: true, 400: false, 404: false} {
		codes["/queue/join"] = code
		_, err := c.Join(ctx, "e", "c")
		if err == nil || IsRetryable(err) != retry {
			t.Errorf("HTTP %d: err=%v retryable=%v want %v", code, err, IsRetryable(err), retry)
		}
	}
	_, err := c.Status(ctx, "q_1", 0)
	if !IsUnknownQueueID(err) || IsRetryable(err) {
		t.Fatalf("status 404 = %v", err)
	}
	if r.bad != 6 || r.ok != 0 {
		t.Fatalf("recorded ok=%d bad=%d", r.ok, r.bad)
	}

	// Connection refused is retryable and recorded as an error.
	srv.Close()
	if _, err := c.Join(ctx, "e", "c"); !IsRetryable(err) {
		t.Fatalf("network error = %v", err)
	}
	if r.bad != 7 {
		t.Fatalf("network error not recorded: %d", r.bad)
	}
}

func TestAbortedLongPollIsNotRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	r := &rec{}
	c, _ := NewClient(srv.URL, NewHTTPClient(10), r)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Status(ctx, "q_1", 15)
	if err == nil || IsRetryable(err) || time.Since(start) > time.Second {
		t.Fatalf("err=%v after %s", err, time.Since(start))
	}
	if r.ok+r.bad != 0 {
		t.Fatal("aborted request must not be recorded")
	}
}

func TestBaseURLValidation(t *testing.T) {
	for _, u := range []string{"ftp://x", "http://", "http://u:p@x", "http://x/?q=1"} {
		if _, err := NewClient(u, NewHTTPClient(1), nil); err == nil {
			t.Errorf("NewClient(%q) should fail", u)
		}
	}
}
