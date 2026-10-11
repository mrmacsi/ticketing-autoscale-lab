package config

import (
	"strings"
	"testing"
)

func valid() TrafficConfig {
	return TrafficConfig{
		EventID: "evt", Seed: 1, DurationSeconds: 600, MaxVUs: 5000,
		Pattern:   Pattern{Type: PatternRampUp, StartRate: 5, EndRate: 900},
		Mix:       Mix{Browse: 40, Availability: 20, Reserve: 25, Purchase: 15},
		Behaviour: Behaviour{MaxQuantity: 12, ThinkTimeMinMs: 500, ThinkTimeMaxMs: 100},
	}
}

func TestValidate(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	c := valid()
	c.EventID = ""
	c.Mix.Browse = 10
	c.Pattern.Type = "sawtooth"
	c.Behaviour.DuplicateSubmitProbability = 1.5
	err := c.Validate()
	for _, want := range []string{"eventId", "sum to 100", "sawtooth", "duplicateSubmitProbability"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v should mention %q", err, want)
		}
	}
	c = valid()
	c.Pattern = Pattern{Type: PatternCustom}
	if c.Validate() == nil {
		t.Error("custom pattern without points must be invalid")
	}
}

func TestClamp(t *testing.T) {
	c, notes := valid().Clamp(Limits{MaxRate: 500, MaxVUs: 2000, MaxDurationSeconds: 300})
	if c.DurationSeconds != 300 || c.MaxVUs != 2000 || c.RequestTimeoutMs != 10000 {
		t.Fatalf("clamped = %+v", c)
	}
	if c.Behaviour.MaxQuantity != MaxReservationQuantity || c.Behaviour.ThinkTimeMaxMs != 500 {
		t.Fatalf("behaviour = %+v", c.Behaviour)
	}
	if len(notes) != 3 {
		t.Fatalf("notes = %v", notes)
	}
	d := valid()
	d.MaxVUs = 0
	if got, _ := d.Clamp(DefaultLimits()); got.MaxVUs != 1_000_000 {
		t.Fatalf("maxVUs default = %d", got.MaxVUs)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	if _, err := Parse([]byte(`{"eventId":"e","durationSecs":5}`)); err == nil {
		t.Fatal("unknown field must fail")
	}
	c, err := Parse([]byte(`{"eventId":"e","seed":3,"pattern":{"type":"constant","rate":2.5},"mix":{"purchase":100}}`))
	if err != nil || c.Pattern.Rate != 2.5 || c.Mix.Purchase != 100 || c.Seed != 3 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestWaitingRoomConfig(t *testing.T) {
	c, err := Parse([]byte(`{"eventId":"e","pattern":{"type":"constant","rate":1},"mix":{"purchase":100},"waitingRoom":{"enabled":true}}`))
	if err != nil || !c.WaitingRoomEnabled() {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := Parse([]byte(`{"eventId":"e","waitingRoom":{"enabled":true,"activeUsersPerServer":25}}`)); err == nil {
		t.Fatal("unknown waitingRoom field must fail in local configs")
	}
	for _, raw := range []string{`{"eventId":"e"}`, `{"eventId":"e","waitingRoom":{"enabled":false}}`, `{"eventId":"e","waitingRoom":null}`} {
		c, err := Parse([]byte(raw))
		if err != nil || c.WaitingRoomEnabled() {
			t.Fatalf("%s: enabled=%v err=%v", raw, c.WaitingRoomEnabled(), err)
		}
	}
}
