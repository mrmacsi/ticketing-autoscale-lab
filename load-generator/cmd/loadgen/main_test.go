package main

import (
	"strings"
	"testing"
)

func TestCheckTarget(t *testing.T) {
	allowed := []string{"34.1.2.3", "lab.example.test:8080"}
	cases := []struct {
		url      string
		loopback bool
		ok       bool
	}{
		{"http://34.1.2.3", false, true},
		{"http://34.1.2.3:80/", false, true},
		{"http://lab.example.test:8080", false, true},
		{"http://lab.example.test", false, false},
		{"http://evil.example.com", false, false},
		{"http://localhost:8080", false, false},
		{"http://localhost:8080", true, true},
		{"http://127.0.0.1:9", true, true},
		{"http://[::1]:9", true, true},
		{"http://10.0.0.1", true, false},
	}
	for _, c := range cases {
		if err := checkTarget(c.url, allowed, c.loopback); (err == nil) != c.ok {
			t.Errorf("checkTarget(%q, loopback=%v) = %v, want ok=%v", c.url, c.loopback, err, c.ok)
		}
	}
}

func TestLimitsFromEnv(t *testing.T) {
	t.Setenv("MAX_RATE", "250")
	t.Setenv("MAX_VUS", "100")
	t.Setenv("MAX_DURATION_SECONDS", "60")
	l, err := limitsFromEnv()
	if err != nil || l.MaxRate != 250 || l.MaxVUs != 100 || l.MaxDurationSeconds != 60 {
		t.Fatalf("%+v %v", l, err)
	}
	t.Setenv("MAX_VUS", "-1")
	if _, err := limitsFromEnv(); err == nil {
		t.Fatal("negative MAX_VUS must fail")
	}
}

func TestAgentIDFromEnv(t *testing.T) {
	t.Setenv("AGENT_ID", "fixed")
	if got := agentIDFromEnv(t.Context()); got != "fixed" {
		t.Fatalf("agent id = %q", got)
	}
}

func TestQueueBaseURL(t *testing.T) {
	allowed := []string{"34.1.2.3", "queue.example.test"}
	// Default: the target itself.
	if got, err := queueBaseURL("", "http://34.1.2.3", allowed, false); err != nil || got != "http://34.1.2.3" {
		t.Fatalf("default = %q %v", got, err)
	}
	if got, err := queueBaseURL(" http://queue.example.test ", "http://34.1.2.3", allowed, false); err != nil || got != "http://queue.example.test" {
		t.Fatalf("explicit = %q %v", got, err)
	}
	for _, bad := range []string{"http://evil.example.com", "http://localhost:8081", "ftp://34.1.2.3", "http://34.1.2.3/?x=1"} {
		if _, err := queueBaseURL(bad, "http://34.1.2.3", allowed, false); err == nil {
			t.Errorf("queueBaseURL(%q) must be refused", bad)
		}
	}
	// Local runs may use loopback, like the target.
	if _, err := queueBaseURL("http://127.0.0.1:8081", "http://127.0.0.1:8080", nil, true); err != nil {
		t.Fatal(err)
	}
}

func TestAgentRefusesDisallowedQueueHost(t *testing.T) {
	t.Setenv("TARGET_BASE_URL", "http://34.1.2.3")
	t.Setenv("ALLOWED_TARGET_HOSTS", "34.1.2.3")
	t.Setenv("QUEUE_BASE_URL", "http://evil.example.com")
	err := runAgent(t.Context())
	if err == nil || !strings.Contains(err.Error(), "QUEUE_BASE_URL") {
		t.Fatalf("runAgent = %v", err)
	}
}
