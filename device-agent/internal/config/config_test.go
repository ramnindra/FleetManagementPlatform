package config

import (
	"os"
	"path/filepath"
	"testing"
)

const sample = `
device:
  id: gpu-node-001
  type: gpu
controller:
  api_endpoint: "http://localhost:8000"
  mqtt_host: "localhost"
  mqtt_port: 1883
heartbeat:
  interval_seconds: 15
logging:
  level: DEBUG
agent:
  credential_path: "/tmp/cred"
enrollment:
  token: "tok-123"
telemetry:
  simulate_gpu: true
`

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadParsesAllFields(t *testing.T) {
	c, err := Load(write(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if c.DeviceID != "gpu-node-001" || c.DeviceType != "gpu" || c.MQTTPort != 1883 ||
		c.HeartbeatIntervalSeconds() != 15 || c.EnrollmentToken != "tok-123" || !c.SimulateGPU || c.LogLevel != "DEBUG" {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(write(t, "device: {id: d, type: edge}\ncontroller: {api_endpoint: http://x, mqtt_host: h}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.MQTTPort != 1883 || c.HeartbeatIntervalSeconds() != 30 || !c.SimulateGPU || c.CredentialPath != "/etc/device-agent/d.credential" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestMissingRequiredField(t *testing.T) {
	if _, err := Load(write(t, "device:\n  id: x\ncontroller:\n  mqtt_host: h\n  mqtt_port: 1\n")); err == nil {
		t.Fatal("expected error")
	}
}

func TestCredentialRoundTripAndPermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "device.credential")
	if c, err := LoadCredential(p); err != nil || c != "" {
		t.Fatalf("missing credential: %q %v", c, err)
	}
	if err := SaveCredential(p, "super-secret-credential"); err != nil {
		t.Fatal(err)
	}
	if c, _ := LoadCredential(p); c != "super-secret-credential" {
		t.Fatalf("got %q", c)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode().Perm())
	}
}
