// Package fsstore is the thin Firestore adapter for the controller Store and
// the traffic bucket writer. All decision logic lives in the controller; this
// package only maps documents to Go values.
package fsstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/config"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/controller"
	"github.com/mrmacsi/ticketing-autoscale-lab/load-generator/metrics"
)

const (
	collGenerator   = "generator"
	docCommand      = "command"
	docStatus       = "status"
	collExperiments = "experiments"
	collTraffic     = "traffic"
)

// Store implements controller.Store on Firestore.
type Store struct {
	client *firestore.Client
}

var _ controller.Store = (*Store)(nil)

// New wraps a Firestore client.
func New(client *firestore.Client) *Store { return &Store{client: client} }

// WatchCommands streams snapshots of generator/command.
func (s *Store) WatchCommands(ctx context.Context, fn func(controller.Command)) error {
	it := s.client.Collection(collGenerator).Doc(docCommand).Snapshots(ctx)
	defer it.Stop()
	for {
		snap, err := it.Next()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if snap == nil || !snap.Exists() {
			continue
		}
		fn(DecodeCommand(snap.Data()))
	}
}

// DecodeCommand converts a generator/command document.
func DecodeCommand(m map[string]any) controller.Command {
	cmd := controller.Command{
		Seq:          toInt64(m["seq"]),
		Action:       toString(m["action"]),
		ExperimentID: toString(m["experimentId"]),
		IssuedBy:     toString(m["issuedBy"]),
	}
	if t, ok := m["issuedAt"].(time.Time); ok {
		cmd.IssuedAt = t
	}
	if raw, ok := m["config"]; ok && raw != nil {
		var cfg config.TrafficConfig
		buf, err := json.Marshal(raw)
		if err == nil {
			err = json.Unmarshal(buf, &cfg)
		}
		if err != nil {
			cmd.DecodeErr = fmt.Errorf("decode config: %w", err)
		} else {
			cmd.Config = &cfg
		}
	}
	return cmd
}

// LoadStatus reads generator/status.
func (s *Store) LoadStatus(ctx context.Context) (controller.Status, bool, error) {
	snap, err := s.client.Collection(collGenerator).Doc(docStatus).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return controller.Status{}, false, nil
	}
	if err != nil {
		return controller.Status{}, false, err
	}
	m := snap.Data()
	st := controller.Status{
		AgentID:        toString(m["agentId"]),
		State:          controller.State(toString(m["state"])),
		ExperimentID:   toString(m["experimentId"]),
		LastCommandSeq: toInt64(m["lastCommandSeq"]),
		Message:        toString(m["message"]),
		Version:        toString(m["version"]),
		TargetBaseURL:  toString(m["targetBaseUrl"]),
	}
	if t, ok := m["heartbeatAt"].(time.Time); ok {
		st.HeartbeatAt = t
	}
	if t, ok := m["runStartedAt"].(time.Time); ok {
		st.RunStartedAt = &t
	}
	return st, true, nil
}

// WriteStatus overwrites generator/status.
func (s *Store) WriteStatus(ctx context.Context, st controller.Status) error {
	_, err := s.client.Collection(collGenerator).Doc(docStatus).Set(ctx, EncodeStatus(st))
	return err
}

// EncodeStatus converts a Status to its document form.
func EncodeStatus(st controller.Status) map[string]any {
	return map[string]any{
		"agentId":        st.AgentID,
		"state":          string(st.State),
		"experimentId":   nullableString(st.ExperimentID),
		"lastCommandSeq": st.LastCommandSeq,
		"heartbeatAt":    st.HeartbeatAt,
		"runStartedAt":   nullableTime(st.RunStartedAt),
		"message":        st.Message,
		"version":        st.Version,
		"limits": map[string]any{
			"maxRate":            st.Limits.MaxRate,
			"maxVUs":             st.Limits.MaxVUs,
			"maxDurationSeconds": st.Limits.MaxDurationSeconds,
		},
		"targetBaseUrl": st.TargetBaseURL,
	}
}

// UpdateGenerator runs fn on experiments/{id}.generator in a transaction and
// writes only the generator field (control-api owns the rest of the document).
func (s *Store) UpdateGenerator(ctx context.Context, experimentID string, fn controller.GeneratorUpdate) error {
	ref := s.client.Collection(collExperiments).Doc(experimentID)
	return s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		exists := true
		if status.Code(err) == codes.NotFound {
			exists = false
		} else if err != nil {
			return err
		}
		var cur controller.Generator
		if exists {
			if m, ok := snap.Data()["generator"].(map[string]any); ok {
				cur = DecodeGenerator(m)
			}
		}
		next, err := fn(cur, exists)
		if err != nil {
			return err
		}
		return tx.Update(ref, []firestore.Update{{Path: "generator", Value: EncodeGenerator(next)}})
	})
}

// DecodeGenerator converts the generator map.
func DecodeGenerator(m map[string]any) controller.Generator {
	g := controller.Generator{
		Owner:        toString(m["owner"]),
		State:        toString(m["state"]),
		FinishReason: toString(m["finishReason"]),
		Message:      toString(m["message"]),
	}
	if t, ok := m["leaseExpiresAt"].(time.Time); ok {
		g.LeaseExpiresAt = t
	}
	if t, ok := m["startedAt"].(time.Time); ok {
		g.StartedAt = &t
	}
	if t, ok := m["finishedAt"].(time.Time); ok {
		g.FinishedAt = &t
	}
	return g
}

// EncodeGenerator converts a Generator to the document map.
func EncodeGenerator(g controller.Generator) map[string]any {
	return map[string]any{
		"owner":          g.Owner,
		"leaseExpiresAt": g.LeaseExpiresAt,
		"state":          g.State,
		"startedAt":      nullableTime(g.StartedAt),
		"finishedAt":     nullableTime(g.FinishedAt),
		"finishReason":   nullableString(g.FinishReason),
		"message":        g.Message,
	}
}

// TrafficWriter returns a metrics.WriteFunc that stores each bucket as
// experiments/{id}/traffic/{second:06d}.
func (s *Store) TrafficWriter(experimentID string) metrics.WriteFunc {
	coll := s.client.Collection(collExperiments).Doc(experimentID).Collection(collTraffic)
	return func(ctx context.Context, b metrics.Bucket) error {
		m, err := BucketDocument(b)
		if err != nil {
			return err
		}
		_, err = coll.Doc(fmt.Sprintf("%06d", b.Second)).Set(ctx, m)
		return err
	}
}

// BucketDocument converts a bucket to a Firestore map: integers stay
// integers, floats stay doubles, and ts becomes a native Timestamp.
func BucketDocument(b metrics.Bucket) (map[string]any, error) {
	buf, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	m, ok := normalize(raw).(map[string]any)
	if !ok {
		return nil, errors.New("bucket did not encode to an object")
	}
	m["ts"] = b.TS
	return m, nil
}

func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = normalize(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = normalize(x)
		}
		return t
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		f, _ := t.Float64()
		return f
	default:
		return v
	}
}

func toString(v any) string {
	s, _ := v.(string)
	return s
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}
