// Package server exposes the waiting room over HTTP (docs/waiting-room.md sections 3.1 and 3.4).
package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mrmacsi/ticketing-autoscale-lab/waiting-room/queue"
)

// MaxWait caps the long poll.
const MaxWait = 25 * time.Second

const maxBody = 16 << 10

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Server holds the HTTP handlers.
type Server struct {
	room      *queue.Room
	adminHash [32]byte
	log       *slog.Logger
	mux       *http.ServeMux
	// Version is the build version, reported by /queue/health and the X-App-Version header so a
	// deploy can confirm which image is serving.
	Version string
}

// New builds the handler set. adminToken must not be empty.
func New(room *queue.Room, adminToken string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	s := &Server{room: room, adminHash: sha256.Sum256([]byte(adminToken)), log: log, mux: http.NewServeMux()}
	s.mux.HandleFunc("/queue/join", s.method(http.MethodPost, s.join))
	s.mux.HandleFunc("/queue/status/", s.method(http.MethodGet, s.status))
	s.mux.HandleFunc("/queue/release", s.method(http.MethodPost, s.release))
	s.mux.HandleFunc("/queue/health", s.method(http.MethodGet, s.health))
	s.mux.HandleFunc("/admin/config", s.admin(s.config))
	s.mux.HandleFunc("/admin/reset", s.admin(s.method(http.MethodPost, s.reset)))
	s.mux.HandleFunc("/admin/stats", s.admin(s.method(http.MethodGet, s.stats)))
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "No such endpoint.")
	})
	return s
}

// ServeHTTP sets the common headers and recovers panics.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Served-By", "waiting-room")
	if s.Version != "" {
		h.Set("X-App-Version", s.Version)
	}
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Type", "application/json")
	defer func() {
		if v := recover(); v != nil {
			if v == http.ErrAbortHandler {
				panic(v)
			}
			s.log.Error("handler panic", "path", r.URL.Path, "panic", fmt.Sprint(v))
			writeError(w, http.StatusInternalServerError, "server_error", "Internal error.")
		}
	}()
	s.mux.ServeHTTP(w, r)
}

func (s *Server) method(m string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != m && !(m == http.MethodGet && r.Method == http.MethodHead) {
			w.Header().Set("Allow", m)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use "+m+".")
			return
		}
		next(w, r)
	}
}

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		given, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(strings.TrimSpace(given)))
		if !ok || subtle.ConstantTimeCompare(sum[:], s.adminHash[:]) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Missing or invalid admin token.")
			return
		}
		next(w, r)
	}
}

func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EventID     string `json:"eventId"`
		CustomerRef string `json:"customerRef"`
	}
	if msg := decode(w, r, &body); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	if !uuidRe.MatchString(body.EventID) {
		writeError(w, http.StatusBadRequest, "invalid_request", "eventId must be a UUID.")
		return
	}
	if utf8.RuneCountInString(body.CustomerRef) > 100 || !utf8.ValidString(body.CustomerRef) {
		writeError(w, http.StatusBadRequest, "invalid_request", "customerRef must be at most 100 characters.")
		return
	}
	v, err := s.room.Join(strings.ToLower(body.EventID), body.CustomerRef)
	if errors.Is(err, queue.ErrUnknownEvent) {
		writeError(w, http.StatusNotFound, "unknown_event", "The waiting room is not open for this event.")
		return
	}
	writeData(w, http.StatusOK, v)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/queue/status/")
	if id == "" || len(id) > 64 || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "unknown_queue_id", "Unknown queue id.")
		return
	}
	wait := time.Duration(0)
	if q := r.URL.Query().Get("waitSeconds"); q != "" {
		n, err := strconv.ParseFloat(q, 64)
		if err != nil || n != n {
			writeError(w, http.StatusBadRequest, "invalid_request", "waitSeconds must be a number from 0 to 25.")
			return
		}
		wait = min(max(time.Duration(n*float64(time.Second)), 0), MaxWait)
	}
	v, err := s.room.Status(r.Context(), id, wait)
	switch {
	case errors.Is(err, queue.ErrUnknownQueueID):
		writeError(w, http.StatusNotFound, "unknown_queue_id", "Unknown queue id.")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// Client went away; nothing useful to write.
	case err != nil:
		writeError(w, http.StatusInternalServerError, "server_error", err.Error())
	default:
		writeData(w, http.StatusOK, v)
	}
}

func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	var body struct {
		QueueID string `json:"queueId"`
		Outcome string `json:"outcome"`
	}
	if msg := decode(w, r, &body); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	if body.QueueID == "" || len(body.QueueID) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_request", "queueId is required.")
		return
	}
	if body.Outcome == "" {
		body.Outcome = "abandoned"
	}
	if !queue.Outcomes[body.Outcome] {
		writeError(w, http.StatusBadRequest, "invalid_request", "outcome must be purchased, sold_out, abandoned or failed.")
		return
	}
	writeData(w, http.StatusOK, map[string]bool{"released": s.room.Release(body.QueueID, body.Outcome)})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	body := map[string]string{"status": "ok"}
	if s.Version != "" {
		body["version"] = s.Version
	}
	writeJSON(w, http.StatusOK, body)
}

// configRequest allows partial updates; omitted fields keep their current value.
type configRequest struct {
	Enabled              *bool   `json:"enabled"`
	EventID              *string `json:"eventId"`
	ActiveUsersPerServer *int    `json:"activeUsersPerServer"`
	AdmissionTTLSeconds  *int    `json:"admissionTtlSeconds"`
	SoldOutGraceSeconds  *int    `json:"soldOutGraceSeconds"`
	AdmissionMode        *string `json:"admissionMode"`
	WaitingTimeoutSecs   *int    `json:"waitingTimeoutSeconds"`
	Adaptive             *struct {
		MinPerServer    *int     `json:"minPerServer"`
		MaxPerServer    *int     `json:"maxPerServer"`
		BusyHigh        *float64 `json:"busyHigh"`
		BusyLow         *float64 `json:"busyLow"`
		CPUHigh         *float64 `json:"cpuHigh"`
		CPULow          *float64 `json:"cpuLow"`
		DecreaseFactor  *float64 `json:"decreaseFactor"`
		SlowStartFactor *float64 `json:"slowStartFactor"`
		CooldownSeconds *int     `json:"cooldownSeconds"`
	} `json:"adaptive"`
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		writeData(w, http.StatusOK, s.room.Config())
		return
	case http.MethodPut:
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use PUT.")
		return
	}
	var req configRequest
	if msg := decode(w, r, &req); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	c := s.room.Config()
	if req.Enabled != nil {
		c.Enabled = *req.Enabled
	}
	if req.EventID != nil {
		c.EventID = strings.ToLower(*req.EventID)
	}
	if req.ActiveUsersPerServer != nil {
		c.ActiveUsersPerServer = *req.ActiveUsersPerServer
	}
	if req.AdmissionTTLSeconds != nil {
		c.AdmissionTTLSeconds = *req.AdmissionTTLSeconds
	}
	if req.SoldOutGraceSeconds != nil {
		c.SoldOutGraceSeconds = *req.SoldOutGraceSeconds
	}
	if req.AdmissionMode != nil {
		c.AdmissionMode = *req.AdmissionMode
	}
	if req.WaitingTimeoutSecs != nil {
		c.WaitingTimeoutSeconds = *req.WaitingTimeoutSecs
	}
	if a := req.Adaptive; a != nil {
		set := func(dst *int, v *int) {
			if v != nil {
				*dst = *v
			}
		}
		setF := func(dst *float64, v *float64) {
			if v != nil {
				*dst = *v
			}
		}
		set(&c.Adaptive.MinPerServer, a.MinPerServer)
		set(&c.Adaptive.MaxPerServer, a.MaxPerServer)
		setF(&c.Adaptive.BusyHigh, a.BusyHigh)
		setF(&c.Adaptive.BusyLow, a.BusyLow)
		setF(&c.Adaptive.CPUHigh, a.CPUHigh)
		setF(&c.Adaptive.CPULow, a.CPULow)
		setF(&c.Adaptive.DecreaseFactor, a.DecreaseFactor)
		setF(&c.Adaptive.SlowStartFactor, a.SlowStartFactor)
		set(&c.Adaptive.CooldownSeconds, a.CooldownSeconds)
	}
	if msg := validate(c); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	old := s.room.Config()
	c = s.room.Configure(c)
	s.log.Info("config updated", "enabled", c.Enabled, "eventId", c.EventID,
		"activeUsersPerServer", c.ActiveUsersPerServer, "admissionTtlSeconds", c.AdmissionTTLSeconds,
		"soldOutGraceSeconds", c.SoldOutGraceSeconds, "admissionMode", c.AdmissionMode, "cleared", !c.Enabled || c.EventID != old.EventID)
	writeData(w, http.StatusOK, c)
}

func validate(c queue.Config) string {
	switch {
	case c.EventID != "" && !uuidRe.MatchString(c.EventID):
		return "eventId must be a UUID."
	case c.Enabled && c.EventID == "":
		return "eventId is required when enabled."
	case c.ActiveUsersPerServer < 1 || c.ActiveUsersPerServer > 1000:
		return "activeUsersPerServer must be 1..1000."
	case c.AdmissionTTLSeconds < 10 || c.AdmissionTTLSeconds > 3600:
		return "admissionTtlSeconds must be 10..3600."
	case c.SoldOutGraceSeconds < 0 || c.SoldOutGraceSeconds > 3600:
		return "soldOutGraceSeconds must be 0..3600."
	case c.AdmissionMode != queue.ModeFixed && c.AdmissionMode != queue.ModeAdaptive:
		return "admissionMode must be fixed or adaptive."
	case c.WaitingTimeoutSeconds != 0 && (c.WaitingTimeoutSeconds < 30 || c.WaitingTimeoutSeconds > 3600):
		return "waitingTimeoutSeconds must be 0 (off) or 30..3600 (longer than the 25 s long poll)."
	}
	return c.Adaptive.Validate()
}

func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	s.room.Reset()
	s.log.Info("room reset")
	writeData(w, http.StatusOK, map[string]bool{"reset": true})
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, s.room.Stats())
}

// decode reads a JSON object body; it returns a user-facing message on failure.
func decode(w http.ResponseWriter, r *http.Request, v any) string {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return "Request body too large."
		}
		return "Body must be a JSON object with the documented fields."
	}
	return ""
}

func writeData(w http.ResponseWriter, code int, data any) {
	writeJSON(w, code, map[string]any{"data": data})
}

func writeError(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, map[string]string{"error": errCode, "message": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
