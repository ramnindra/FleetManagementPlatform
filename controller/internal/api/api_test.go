package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ramnindra/fleetmanagementplatform/controller/internal/config"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/metrics"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/service"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/store"
)

type fakePub struct{ published []string }

func (f *fakePub) PublishCommand(deviceID, commandID, action string, _ map[string]any) bool {
	f.published = append(f.published, deviceID+":"+action)
	return true
}

type env struct {
	h   http.Handler
	st  *store.Memory
	pub *fakePub
}

func newEnv(t *testing.T) *env {
	t.Helper()
	cfg := config.Settings{
		AdminAPIKey: "test-admin-key", MQTTWebhookSharedSecret: "test-webhook-secret",
		MQTTClientUsername: "controller-service", MQTTClientPassword: "test-controller-secret",
		EnrollmentTokenTTLSeconds: 3600, OfflineThresholdSeconds: 90, MQTTHost: "emqx", MQTTPort: 1883,
	}
	st := store.NewMemory()
	pub := &fakePub{}
	m := metrics.New(st, 90*time.Second)
	return &env{h: New(cfg, service.New(st), pub, m), st: st, pub: pub}
}

func (e *env) do(method, path string, body any, headers ...string) (*httptest.ResponseRecorder, map[string]any) {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

var (
	admin   = []string{"X-API-Key", "test-admin-key"}
	webhook = []string{"X-Webhook-Secret", "test-webhook-secret"}
)

func (e *env) register(t *testing.T, id string) string {
	t.Helper()
	rec, out := e.do("POST", "/api/v1/devices/register", map[string]any{"device_id": id, "device_type": "switch"}, admin...)
	if rec.Code != 200 {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	return out["enrollment_token"].(string)
}

func (e *env) enroll(t *testing.T, id, token string) string {
	t.Helper()
	rec, out := e.do("POST", "/api/v1/devices/"+id+"/enroll", map[string]any{"enrollment_token": token})
	if rec.Code != 200 {
		t.Fatalf("enroll: %d %s", rec.Code, rec.Body)
	}
	return out["device_credential"].(string)
}

func TestHealth(t *testing.T) {
	e := newEnv(t)
	if rec, _ := e.do("GET", "/healthz", nil); rec.Code != 200 {
		t.Fatalf("healthz %d", rec.Code)
	}
	if rec, _ := e.do("GET", "/readyz", nil); rec.Code != 200 {
		t.Fatalf("readyz %d", rec.Code)
	}
}

func TestRegisterRequiresAdminKey(t *testing.T) {
	e := newEnv(t)
	body := map[string]any{"device_id": "x", "device_type": "switch"}
	if rec, _ := e.do("POST", "/api/v1/devices/register", body); rec.Code != 422 {
		t.Fatalf("missing header: want 422, got %d", rec.Code)
	}
	if rec, _ := e.do("POST", "/api/v1/devices/register", body, "X-API-Key", "nope"); rec.Code != 401 {
		t.Fatalf("bad key: want 401, got %d", rec.Code)
	}
}

func TestRegisterValidationAndConflict(t *testing.T) {
	e := newEnv(t)
	if rec, _ := e.do("POST", "/api/v1/devices/register", map[string]any{"device_id": "a", "device_type": "toaster"}, admin...); rec.Code != 422 {
		t.Fatalf("bad type: got %d", rec.Code)
	}
	e.register(t, "dup")
	if rec, _ := e.do("POST", "/api/v1/devices/register", map[string]any{"device_id": "dup", "device_type": "edge"}, admin...); rec.Code != 409 {
		t.Fatalf("dup: want 409, got %d", rec.Code)
	}
}

func TestFullDeviceLifecycle(t *testing.T) {
	e := newEnv(t)
	token := e.register(t, "gpu-1")
	cred := e.enroll(t, "gpu-1", token)
	if cred == "" {
		t.Fatal("empty credential")
	}
	// token is single-use
	if rec, _ := e.do("POST", "/api/v1/devices/gpu-1/enroll", map[string]any{"enrollment_token": token}); rec.Code != 400 {
		t.Fatalf("re-enroll: want 400, got %d", rec.Code)
	}

	if rec, out := e.do("GET", "/api/v1/devices/gpu-1", nil); rec.Code != 200 || out["status"] != "active" {
		t.Fatalf("get device: %d %v", rec.Code, out)
	}
	if rec, out := e.do("GET", "/api/v1/devices/gpu-1/status", nil); rec.Code != 200 || out["online"] != false {
		t.Fatalf("status: %d %v", rec.Code, out)
	}

	rec, out := e.do("POST", "/api/v1/devices/gpu-1/commands", map[string]any{"action": "ping"})
	if rec.Code != 202 || out["status"] != "delivered" {
		t.Fatalf("create command: %d %v", rec.Code, out)
	}
	if len(e.pub.published) != 1 {
		t.Fatalf("expected 1 publish, got %v", e.pub.published)
	}
	if rec, out := e.do("GET", "/api/v1/commands/"+out["command_id"].(string), nil); rec.Code != 200 || out["action"] != "ping" {
		t.Fatalf("get command: %d %v", rec.Code, out)
	}
	if rec, _ := e.do("POST", "/api/v1/devices/gpu-1/commands", map[string]any{"action": "rm -rf"}); rec.Code != 422 {
		t.Fatalf("disallowed action: got %d", rec.Code)
	}
	if rec, _ := e.do("POST", "/api/v1/devices/nope/commands", map[string]any{"action": "ping"}); rec.Code != 404 {
		t.Fatalf("unknown device: got %d", rec.Code)
	}
}

func TestCommandIdempotency(t *testing.T) {
	e := newEnv(t)
	e.enroll(t, "d1", e.register(t, "d1"))
	body := map[string]any{"action": "ping", "idempotency_key": "k1"}
	_, a := e.do("POST", "/api/v1/devices/d1/commands", body)
	_, b := e.do("POST", "/api/v1/devices/d1/commands", body)
	if a["command_id"] != b["command_id"] {
		t.Fatalf("idempotent calls must return same command: %v vs %v", a, b)
	}
	if len(e.pub.published) != 1 {
		t.Fatalf("duplicate must not republish, got %v", e.pub.published)
	}
}

func TestMQTTAuthWebhook(t *testing.T) {
	e := newEnv(t)
	cred := e.enroll(t, "sw-1", e.register(t, "sw-1"))

	auth := func(user, pass, client string) map[string]any {
		_, out := e.do("POST", "/internal/mqtt/auth",
			map[string]any{"username": user, "password": pass, "clientid": client}, webhook...)
		return out
	}
	if auth("sw-1", cred, "sw-1")["result"] != "allow" {
		t.Fatal("valid device should be allowed")
	}
	if auth("sw-1", "bad", "sw-1")["result"] != "deny" {
		t.Fatal("bad credential should be denied")
	}
	if auth("sw-1", cred, "other")["result"] != "deny" {
		t.Fatal("clientid mismatch should be denied")
	}
	if out := auth("controller-service", "test-controller-secret", "c"); out["result"] != "allow" || out["is_superuser"] != true {
		t.Fatalf("controller service should be superuser: %v", out)
	}
	if rec, _ := e.do("POST", "/internal/mqtt/auth", map[string]any{}, "X-Webhook-Secret", "wrong"); rec.Code != 401 {
		t.Fatalf("bad webhook secret: got %d", rec.Code)
	}
}

func TestMQTTACLWebhook(t *testing.T) {
	e := newEnv(t)
	acl := func(topic, action string) any {
		_, out := e.do("POST", "/internal/mqtt/acl",
			map[string]any{"username": "sw-1", "clientid": "sw-1", "topic": topic, "action": action}, webhook...)
		return out["result"]
	}
	cases := []struct{ topic, action, want string }{
		{"devices/sw-1/heartbeat", "publish", "allow"},
		{"devices/sw-1/cmd/ack", "publish", "allow"},
		{"devices/sw-1/cmd", "subscribe", "allow"},
		{"devices/sw-1/cmd", "publish", "deny"},
		{"devices/sw-2/heartbeat", "publish", "deny"},
		{"devices/sw-1/heartbeat", "subscribe", "deny"},
	}
	for _, c := range cases {
		if got := acl(c.topic, c.action); got != c.want {
			t.Errorf("%s %s: want %s got %v", c.action, c.topic, c.want, got)
		}
	}
}

func TestRotateAndRevoke(t *testing.T) {
	e := newEnv(t)
	oldCred := e.enroll(t, "sw-2", e.register(t, "sw-2"))

	rec, out := e.do("POST", "/api/v1/devices/sw-2/credentials/rotate", nil, admin...)
	if rec.Code != 200 {
		t.Fatalf("rotate: %d", rec.Code)
	}
	_, auth := e.do("POST", "/internal/mqtt/auth", map[string]any{"username": "sw-2", "password": oldCred, "clientid": "sw-2"}, webhook...)
	if auth["result"] != "deny" {
		t.Fatal("old credential must be rejected after rotation")
	}
	e.enroll(t, "sw-2", out["enrollment_token"].(string))

	if rec, out := e.do("POST", "/api/v1/devices/sw-2/revoke", nil, admin...); rec.Code != 200 || out["status"] != "revoked" {
		t.Fatalf("revoke: %d %v", rec.Code, out)
	}
	if rec, _ := e.do("POST", "/api/v1/devices/sw-2/credentials/rotate", nil, admin...); rec.Code != 400 {
		t.Fatalf("rotate revoked: got %d", rec.Code)
	}
	if rec, _ := e.do("POST", "/api/v1/devices/ghost/revoke", nil, admin...); rec.Code != 404 {
		t.Fatalf("revoke unknown: got %d", rec.Code)
	}
}

func TestListAndMessages(t *testing.T) {
	e := newEnv(t)
	e.register(t, "ui-1")
	if rec, _ := e.do("GET", "/api/v1/devices/nope/messages", nil); rec.Code != 404 {
		t.Fatalf("messages unknown: %d", rec.Code)
	}
	if rec, _ := e.do("GET", "/api/v1/devices/nope/commands", nil); rec.Code != 404 {
		t.Fatalf("commands unknown: %d", rec.Code)
	}
	svc := service.New(e.st)
	_ = svc.RecordMessage(t.Context(), "ui-1", "heartbeat", map[string]any{"n": 1})
	_ = svc.RecordMessage(t.Context(), "ui-1", "telemetry", map[string]any{"n": 2})

	req := httptest.NewRequest("GET", "/api/v1/devices/ui-1/messages?after_id=1", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var msgs []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &msgs)
	if len(msgs) != 1 || msgs[0]["kind"] != "telemetry" {
		t.Fatalf("after_id filter: %v", msgs)
	}

	req = httptest.NewRequest("GET", "/api/v1/devices", nil)
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if !strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "[") {
		t.Fatalf("list devices should be a JSON array: %s", rec.Body)
	}
}

func TestMetricsReportLiveDeviceCounts(t *testing.T) {
	e := newEnv(t)
	e.register(t, "m-1")
	_ = e.st.TouchLastSeen(t.Context(), "m-1", time.Now())
	e.register(t, "m-2")

	rec, _ := e.do("GET", "/metrics", nil)
	body := rec.Body.String()
	for _, want := range []string{"controller_devices_online 1", "controller_devices_offline 1"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

func TestWebUIServed(t *testing.T) {
	e := newEnv(t)
	rec, _ := e.do("GET", "/ui/", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("ui: %d", rec.Code)
	}
}

func TestListRecentCommandsAcrossDevices(t *testing.T) {
	e := newEnv(t)
	e.enroll(t, "a", e.register(t, "a"))
	e.enroll(t, "b", e.register(t, "b"))
	e.do("POST", "/api/v1/devices/a/commands", map[string]any{"action": "ping"})
	e.do("POST", "/api/v1/devices/b/commands", map[string]any{"action": "get_status"})

	req := httptest.NewRequest("GET", "/api/v1/commands?limit=10", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var cmds []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &cmds)
	if rec.Code != 200 || len(cmds) != 2 || cmds[0]["device_id"] != "b" {
		t.Fatalf("want 2 commands newest first, got %d %v", rec.Code, cmds)
	}
}
