package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ramnindra/fleetmanagementplatform/device-agent/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte("device: {id: edge-1, type: edge}\ncontroller: {api_endpoint: http://x, mqtt_host: h}\n"), 0o600)
	c, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPing(t *testing.T) {
	st, r := Execute(testConfig(t), "ping", map[string]any{"echo": "hi"})
	if st != "success" || r["pong"] != true || r["echo"] != "hi" {
		t.Fatalf("%s %v", st, r)
	}
}

func TestGetStatus(t *testing.T) {
	st, r := Execute(testConfig(t), "get_status", nil)
	if st != "success" || r["status"] != "healthy" || r["device_id"] != "edge-1" {
		t.Fatalf("%s %v", st, r)
	}
}

func TestGetSystemInfoIncludesTelemetry(t *testing.T) {
	st, r := Execute(testConfig(t), "get_system_info", nil)
	if st != "success" || r["telemetry"] == nil || r["hostname"] == nil {
		t.Fatalf("%s %v", st, r)
	}
}

func TestUnknownActionRejected(t *testing.T) {
	st, r := Execute(testConfig(t), "rm -rf /", nil)
	if st != "failed" || r["error"] == nil {
		t.Fatalf("%s %v", st, r)
	}
}

func TestUpdateConfigAppliesAllowedKey(t *testing.T) {
	c := testConfig(t)
	st, r := Execute(c, "update_config", map[string]any{"heartbeat_interval_seconds": float64(60)})
	if st != "success" || r["applied"].(map[string]any)["heartbeat_interval_seconds"] != 60 || c.HeartbeatIntervalSeconds() != 60 {
		t.Fatalf("%s %v interval=%d", st, r, c.HeartbeatIntervalSeconds())
	}
}

func TestUpdateConfigRejectsDisallowedKey(t *testing.T) {
	c := testConfig(t)
	st, r := Execute(c, "update_config", map[string]any{"credential_path": "/tmp/pwned"})
	rej := r["rejected"].([]string)
	if st != "success" || len(r["applied"].(map[string]any)) != 0 || len(rej) != 1 || rej[0] != "credential_path" {
		t.Fatalf("%s %v", st, r)
	}
	if c.CredentialPath == "/tmp/pwned" {
		t.Fatal("credential_path was modified")
	}
}

func TestUpdateConfigRejectsOutOfRange(t *testing.T) {
	c := testConfig(t)
	for _, v := range []any{float64(999999), float64(1), "30", 12.5} {
		_, r := Execute(c, "update_config", map[string]any{"heartbeat_interval_seconds": v})
		if len(r["applied"].(map[string]any)) != 0 {
			t.Fatalf("value %v should be rejected: %v", v, r)
		}
	}
	if c.HeartbeatIntervalSeconds() != 30 {
		t.Fatal("interval changed")
	}
}
