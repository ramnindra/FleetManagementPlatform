// Package api implements the controller's REST API and EMQX webhooks.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ramnindra/fleetmanagementplatform/controller/internal/config"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/metrics"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/model"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/service"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/store"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/ui"
)

// Publisher publishes commands to devices over MQTT.
type Publisher interface {
	PublishCommand(deviceID, commandID, action string, params map[string]any) bool
}

type Server struct {
	cfg config.Settings
	svc *service.Service
	pub Publisher
	m   *metrics.Metrics
}

func New(cfg config.Settings, svc *service.Service, pub Publisher, m *metrics.Metrics) http.Handler {
	s := &Server{cfg: cfg, svc: svc, pub: pub, m: m}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/devices/register", s.admin(s.registerDevice))
	mux.HandleFunc("POST /api/v1/devices/{device_id}/enroll", s.enrollDevice)
	mux.HandleFunc("POST /api/v1/devices/{device_id}/credentials/rotate", s.admin(s.rotateCredential))
	mux.HandleFunc("POST /api/v1/devices/{device_id}/revoke", s.admin(s.revokeDevice))
	mux.HandleFunc("GET /api/v1/devices", s.listDevices)
	mux.HandleFunc("GET /api/v1/devices/{device_id}", s.getDevice)
	mux.HandleFunc("GET /api/v1/devices/{device_id}/status", s.getDeviceStatus)
	mux.HandleFunc("GET /api/v1/devices/{device_id}/commands", s.listDeviceCommands)
	mux.HandleFunc("GET /api/v1/devices/{device_id}/messages", s.listDeviceMessages)
	mux.HandleFunc("POST /api/v1/devices/{device_id}/commands", s.createCommand)
	mux.HandleFunc("GET /api/v1/commands/{command_id}", s.getCommand)

	mux.HandleFunc("POST /internal/mqtt/auth", s.webhook(s.mqttAuth))
	mux.HandleFunc("POST /internal/mqtt/acl", s.webhook(s.mqttACL))

	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))

	mux.Handle("GET /ui/", http.StripPrefix("/ui/", http.FileServerFS(ui.FS())))
	mux.HandleFunc("GET /ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
	})

	return s.instrument(mux)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, "invalid request body: "+err.Error())
		return false
	}
	return true
}

func intQuery(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func clamp(n, lo, hi int) int { return max(lo, min(n, hi)) }

func secretEquals(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }

// instrument records latency by route template (r.Pattern), never raw path.
func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		path := "unmatched"
		if r.Pattern != "" {
			path = r.Pattern
			if _, p, ok := strings.Cut(r.Pattern, " "); ok {
				path = p
			}
		}
		s.m.HTTPDuration.WithLabelValues(r.Method, path, strconv.Itoa(rec.status)).Observe(time.Since(start).Seconds())
	})
}

// headerGuard rejects a missing header with 422 (as before) and a wrong value with 401.
func headerGuard(header, want, what string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(header)
		if got == "" {
			writeErr(w, http.StatusUnprocessableEntity, "missing header "+header)
			return
		}
		if !secretEquals(got, want) {
			writeErr(w, http.StatusUnauthorized, "invalid "+what)
			return
		}
		next(w, r)
	}
}

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return headerGuard("X-API-Key", s.cfg.AdminAPIKey, "API key", next)
}

func (s *Server) webhook(next http.HandlerFunc) http.HandlerFunc {
	return headerGuard("X-Webhook-Secret", s.cfg.MQTTWebhookSharedSecret, "webhook secret", next)
}

type registerResponse struct {
	DeviceID                 string    `json:"device_id"`
	Status                   string    `json:"status"`
	EnrollmentToken          string    `json:"enrollment_token"`
	EnrollmentTokenExpiresAt time.Time `json:"enrollment_token_expires_at"`
}

func newRegisterResponse(d *model.Device, token string) registerResponse {
	return registerResponse{d.DeviceID, d.Status, token, *d.EnrollmentTokenExpiresAt}
}

// --- device handlers ---

func (s *Server) registerDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceID   string         `json:"device_id"`
		DeviceType string         `json:"device_type"`
		Attributes map[string]any `json:"attributes"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if n := len(req.DeviceID); n < 1 || n > 128 {
		writeErr(w, http.StatusUnprocessableEntity, "device_id must be 1-128 characters")
		return
	}
	if !model.DeviceTypes[req.DeviceType] {
		writeErr(w, http.StatusUnprocessableEntity, "device_type must be one of: switch, gpu, edge")
		return
	}
	d, token, err := s.svc.RegisterDevice(r.Context(), req.DeviceID, req.DeviceType, req.Attributes,
		time.Duration(s.cfg.EnrollmentTokenTTLSeconds)*time.Second)
	switch {
	case errors.Is(err, service.ErrDeviceConflict):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		s.internalError(w, err)
	default:
		writeJSON(w, http.StatusOK, newRegisterResponse(d, token))
	}
}

func (s *Server) enrollDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EnrollmentToken string `json:"enrollment_token"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	id := r.PathValue("device_id")
	_, cred, err := s.svc.EnrollDevice(r.Context(), id, req.EnrollmentToken)
	if s.enrollmentFailure(w, err, http.StatusBadRequest) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id":         id,
		"device_credential": cred,
		"mqtt_host":         s.cfg.MQTTHost,
		"mqtt_port":         s.cfg.MQTTPort,
		"topics": map[string]string{
			"heartbeat": fmt.Sprintf("devices/%s/heartbeat", id),
			"telemetry": fmt.Sprintf("devices/%s/telemetry", id),
			"command":   fmt.Sprintf("devices/%s/cmd", id),
			"ack":       fmt.Sprintf("devices/%s/cmd/ack", id),
		},
	})
}

// enrollmentFailure writes a response and returns true if err is non-nil.
func (s *Server) enrollmentFailure(w http.ResponseWriter, err error, status int) bool {
	var ee *service.EnrollmentError
	switch {
	case err == nil:
		return false
	case errors.As(err, &ee):
		writeErr(w, status, ee.Msg)
	default:
		s.internalError(w, err)
	}
	return true
}

func (s *Server) rotateCredential(w http.ResponseWriter, r *http.Request) {
	d, token, err := s.svc.RotateCredential(r.Context(), r.PathValue("device_id"),
		time.Duration(s.cfg.EnrollmentTokenTTLSeconds)*time.Second)
	if s.enrollmentFailure(w, err, http.StatusBadRequest) {
		return
	}
	writeJSON(w, http.StatusOK, newRegisterResponse(d, token))
}

func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request) {
	d, err := s.svc.RevokeDevice(r.Context(), r.PathValue("device_id"))
	if s.enrollmentFailure(w, err, http.StatusNotFound) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"device_id": d.DeviceID, "status": d.Status})
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := s.svc.ListDevices(r.Context(), clamp(intQuery(r, "limit", 100), 1, 1000), max(intQuery(r, "offset", 0), 0))
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, devices)
}

// device loads the path's device, writing 404/500 itself on failure.
func (s *Server) device(w http.ResponseWriter, r *http.Request) *model.Device {
	d, err := s.svc.GetDevice(r.Context(), r.PathValue("device_id"))
	switch {
	case errors.Is(err, service.ErrDeviceNotFound):
		writeErr(w, http.StatusNotFound, "device not found")
	case err != nil:
		s.internalError(w, err)
	default:
		return d
	}
	return nil
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	if d := s.device(w, r); d != nil {
		writeJSON(w, http.StatusOK, d)
	}
}

func (s *Server) getDeviceStatus(w http.ResponseWriter, r *http.Request) {
	d := s.device(w, r)
	if d == nil {
		return
	}
	online, age := service.IsOnline(d, time.Duration(s.cfg.OfflineThresholdSeconds)*time.Second)
	writeJSON(w, http.StatusOK, map[string]any{
		"device_id": d.DeviceID, "status": d.Status, "online": online,
		"last_seen_at": d.LastSeenAt, "seconds_since_last_seen": age,
	})
}

func (s *Server) listDeviceCommands(w http.ResponseWriter, r *http.Request) {
	d := s.device(w, r)
	if d == nil {
		return
	}
	cmds, err := s.svc.ListCommands(r.Context(), d.DeviceID, clamp(intQuery(r, "limit", 50), 1, 200))
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cmds)
}

func (s *Server) listDeviceMessages(w http.ResponseWriter, r *http.Request) {
	d := s.device(w, r)
	if d == nil {
		return
	}
	msgs, err := s.svc.ListMessages(r.Context(), d.DeviceID, clamp(intQuery(r, "limit", 50), 1, 200), int64(intQuery(r, "after_id", 0)))
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

// --- commands ---

func (s *Server) createCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action         string         `json:"action"`
		Params         map[string]any `json:"params"`
		IdempotencyKey *string        `json:"idempotency_key"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if !model.AllowedActions[req.Action] {
		writeErr(w, http.StatusUnprocessableEntity, "action must be one of: get_status, get_system_info, ping, update_config")
		return
	}
	if req.IdempotencyKey != nil && len(*req.IdempotencyKey) > 128 {
		writeErr(w, http.StatusUnprocessableEntity, "idempotency_key must be at most 128 characters")
		return
	}
	deviceID := r.PathValue("device_id")
	cmd, created, err := s.svc.CreateCommand(r.Context(), deviceID, req.Action, req.Params, req.IdempotencyKey)
	switch {
	case errors.Is(err, service.ErrDeviceNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		s.internalError(w, err)
		return
	}
	if created {
		// "delivered" means the broker accepted the publish (QoS1 PUBACK), not
		// that the device has processed it — see mqttc.PublishCommand.
		if s.pub.PublishCommand(deviceID, cmd.CommandID, cmd.Action, cmd.Params) {
			if err := s.svc.MarkDelivered(r.Context(), cmd.CommandID); err == nil {
				cmd.Status = model.CmdDelivered
			}
		}
	}
	// Re-read so the response reflects any ack that raced the publish.
	if fresh, err := s.svc.GetCommand(r.Context(), cmd.CommandID); err == nil {
		cmd = fresh
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"command_id": cmd.CommandID, "device_id": deviceID, "status": cmd.Status})
}

func (s *Server) getCommand(w http.ResponseWriter, r *http.Request) {
	cmd, err := s.svc.GetCommand(r.Context(), r.PathValue("command_id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "command not found")
	case err != nil:
		s.internalError(w, err)
	default:
		writeJSON(w, http.StatusOK, cmd)
	}
}

// --- EMQX webhooks (see docs/architecture.md: shared secret is a dev-only shortcut) ---

func (s *Server) mqttAuth(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password, ClientID string }
	var raw struct {
		Username string `json:"username"`
		Password string `json:"password"`
		ClientID string `json:"clientid"`
	}
	if !decodeBody(w, r, &raw) {
		return
	}
	req.Username, req.Password, req.ClientID = raw.Username, raw.Password, raw.ClientID

	type resp struct {
		Result      string `json:"result"`
		IsSuperuser bool   `json:"is_superuser"`
	}
	switch {
	case req.Username == s.cfg.MQTTClientUsername:
		if secretEquals(req.Password, s.cfg.MQTTClientPassword) {
			writeJSON(w, http.StatusOK, resp{"allow", true})
		} else {
			writeJSON(w, http.StatusOK, resp{"deny", false})
		}
	case req.Username != req.ClientID:
		writeJSON(w, http.StatusOK, resp{"deny", false})
	case s.svc.AuthenticateDevice(r.Context(), req.Username, req.Password):
		writeJSON(w, http.StatusOK, resp{"allow", false})
	default:
		writeJSON(w, http.StatusOK, resp{"deny", false})
	}
}

func (s *Server) mqttACL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Topic    string `json:"topic"`
		Action   string `json:"action"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Action != "publish" && req.Action != "subscribe" {
		writeErr(w, http.StatusUnprocessableEntity, "action must be publish or subscribe")
		return
	}
	allow := func(ok bool) {
		result := "deny"
		if ok {
			result = "allow"
		}
		writeJSON(w, http.StatusOK, map[string]string{"result": result})
	}
	if req.Username == s.cfg.MQTTClientUsername {
		allow(true)
		return
	}
	prefix := "devices/" + req.Username + "/"
	if !strings.HasPrefix(req.Topic, prefix) {
		allow(false)
		return
	}
	suffix := strings.TrimPrefix(req.Topic, prefix)
	switch req.Action {
	case "publish":
		allow(suffix == "heartbeat" || suffix == "telemetry" || suffix == "cmd/ack")
	case "subscribe":
		allow(suffix == "cmd")
	}
}

// --- health ---

// healthz is liveness: process is up. It does not check dependencies, so a
// transient DB blip doesn't get the pod killed.
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz checks only the database. It deliberately does NOT check this pod's
// MQTT connection: EMQX's auth/ACL webhooks call back into the controller
// through its Service, which only routes to Ready pods — requiring MQTT here
// created a startup deadlock (pod can't be Ready until MQTT connects; MQTT
// can't connect until EMQX's webhook reaches a Ready pod).
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready", "reason": "database"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	slog.Error("internal_error", "error", err)
	writeErr(w, http.StatusInternalServerError, "internal server error")
}
