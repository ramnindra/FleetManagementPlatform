package sim

import (
	"testing"
	"time"
)

func newTestDevice() *Device {
	return NewDevice(0, "gpu", Config{RunID: "t", HeartbeatInterval: 10 * time.Second}, &Metrics{})
}

func TestExecuteAllowlist(t *testing.T) {
	d := newTestDevice()
	if st, r := d.execute("ping", map[string]any{"echo": "x"}); st != "success" || r["pong"] != true || r["echo"] != "x" {
		t.Fatalf("%s %v", st, r)
	}
	if st, _ := d.execute("get_system_info", nil); st != "success" {
		t.Fatal("get_system_info")
	}
	if st, r := d.execute("rm -rf /", nil); st != "failed" || r["error"] == nil {
		t.Fatalf("%s %v", st, r)
	}
}

func TestUpdateConfigChangesHeartbeat(t *testing.T) {
	d := newTestDevice()
	_, r := d.execute("update_config", map[string]any{"heartbeat_interval_seconds": float64(30)})
	if d.HeartbeatInterval() != 30*time.Second || len(r["applied"].(map[string]any)) != 1 {
		t.Fatalf("interval=%v result=%v", d.HeartbeatInterval(), r)
	}
	for _, bad := range []any{float64(1), float64(99999), "30", 7.5} {
		d.execute("update_config", map[string]any{"heartbeat_interval_seconds": bad})
		if d.HeartbeatInterval() != 30*time.Second {
			t.Fatalf("bad value %v changed the interval", bad)
		}
	}
	if _, r := d.execute("update_config", map[string]any{"credential_path": "/x"}); len(r["rejected"].([]string)) != 1 {
		t.Fatal("unknown key must be rejected")
	}
}

func TestTelemetryByType(t *testing.T) {
	for typ, key := range map[string]string{"switch": "ports_up", "gpu": "gpu_utilization", "edge": "cpu_load_percent"} {
		m := Telemetry(typ)
		if m["source"] != "simulated" || m[key] == nil {
			t.Errorf("%s telemetry missing %s: %v", typ, key, m)
		}
	}
}
