package provision

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderBundle(t *testing.T) {
	out := t.TempDir()
	dir, err := RenderBundle(out, "", BundleData{
		DeviceID: "gpu-1", DeviceType: "gpu", APIEndpoint: "http://c:8000", MQTTHost: "emqx", MQTTPort: 1883,
		HeartbeatIntervalSeconds: 15, EnrollmentToken: "tok-abc", SimulateGPU: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	for _, want := range []string{"id: gpu-1", "type: gpu", `token: "tok-abc"`, "interval_seconds: 15", "simulate_gpu: true", "/etc/device-agent/gpu-1.credential"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("config.yaml missing %q:\n%s", want, b)
		}
	}
	if st, _ := os.Stat(filepath.Join(dir, "config.yaml")); st.Mode().Perm() != 0o600 {
		t.Errorf("config.yaml mode = %v, want 0600", st.Mode().Perm())
	}
	if r, _ := os.ReadFile(filepath.Join(dir, "README.txt")); !strings.Contains(string(r), "gpu-1/credentials/rotate") {
		t.Error("README missing device id")
	}
}

func TestLoadInventory(t *testing.T) {
	p := filepath.Join(t.TempDir(), "inv.yaml")
	os.WriteFile(p, []byte("devices:\n  - device_id: a\n    device_type: switch\n    heartbeat_interval_seconds: 15\n    attributes: {site: dc1}\n  - device_id: b\n    device_type: edge\n"), 0o600)
	devs, err := LoadInventory(p)
	if err != nil || len(devs) != 2 || *devs[0].HeartbeatIntervalSeconds != 15 || devs[1].HeartbeatIntervalSeconds != nil {
		t.Fatalf("%v %+v", err, devs)
	}
	os.WriteFile(p, []byte("devices:\n  - device_id: a\n"), 0o600)
	if _, err := LoadInventory(p); err == nil {
		t.Fatal("expected error for missing device_type")
	}
}

func TestClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/devices/known":
			w.Write([]byte(`{}`))
		case r.URL.Path == "/api/v1/devices/register":
			if r.Header.Get("X-Api-Key") != "k" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"enrollment_token":"T"}`))
		case r.URL.Path == "/api/v1/devices/known/status":
			w.Write([]byte(`{"online":true}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k")
	if ok, err := c.IsRegistered("known"); !ok || err != nil {
		t.Fatal("known should be registered")
	}
	if ok, err := c.IsRegistered("nope"); ok || err != nil {
		t.Fatal("nope should not be registered")
	}
	if tok, err := c.Register(InventoryDevice{DeviceID: "x", DeviceType: "edge"}); tok != "T" || err != nil {
		t.Fatalf("%q %v", tok, err)
	}
	if _, err := NewClient(srv.URL, "bad").Register(InventoryDevice{DeviceID: "x", DeviceType: "edge"}); err == nil {
		t.Fatal("bad key should fail")
	}
	if !c.IsOnline("known") || c.IsOnline("nope") {
		t.Fatal("online check wrong")
	}
}
