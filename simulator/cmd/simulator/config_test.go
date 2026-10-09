package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sim.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaults(t *testing.T) {
	s, err := parseSettings(nil)
	if err != nil || s.Count != 10 || s.APIEndpoint != "http://localhost:8000" || s.MQTTHost != "localhost" {
		t.Fatalf("%v %+v", err, s)
	}
}

func TestFlagsOverrideFile(t *testing.T) {
	p := writeCfg(t, "count: 5\ndevice_type: gpu\nheartbeat_interval: 30s\nadmin_api_key: from-file\n")
	s, err := parseSettings([]string{"--config", p, "--count", "7"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Count != 7 || s.DeviceType != "gpu" || s.HeartbeatInterval != 30*time.Second || s.AdminAPIKey != "from-file" {
		t.Fatalf("%+v", s)
	}
}

func TestClusterShorthand(t *testing.T) {
	for _, tc := range []struct{ in, api, host string }{
		{"10.0.0.5", "http://10.0.0.5:8000", "10.0.0.5"},
		{"ctl.example.com:9000", "http://ctl.example.com:9000", "ctl.example.com"},
		{"https://ctl.example.com", "https://ctl.example.com:8000", "ctl.example.com"},
	} {
		s, err := parseSettings([]string{"--cluster", tc.in})
		if err != nil || s.APIEndpoint != tc.api || s.MQTTHost != tc.host {
			t.Errorf("%s: %v api=%s host=%s", tc.in, err, s.APIEndpoint, s.MQTTHost)
		}
	}
	// explicit mqtt-host wins over the shorthand
	s, _ := parseSettings([]string{"--cluster", "10.0.0.5", "--mqtt-host", "broker.internal"})
	if s.MQTTHost != "broker.internal" || s.APIEndpoint != "http://10.0.0.5:8000" {
		t.Fatalf("%+v", s)
	}
}

func TestClusterFromFile(t *testing.T) {
	s, err := parseSettings([]string{"--config", writeCfg(t, "cluster: 10.1.2.3\n")})
	if err != nil || s.APIEndpoint != "http://10.1.2.3:8000" || s.MQTTHost != "10.1.2.3" {
		t.Fatalf("%v %+v", err, s)
	}
}

func TestDeviceTypeMix(t *testing.T) {
	s, err := parseSettings([]string{"--config", writeCfg(t, "device_types: {switch: 2, gpu: 1}\n")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.Types(), []string{"gpu", "switch", "switch"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestValidation(t *testing.T) {
	for _, args := range [][]string{{"--count", "0"}, {"--device-type", "toaster"}, {"--heartbeat-interval", "100ms"}, {"--cluster", "http://"}} {
		if _, err := parseSettings(args); err == nil {
			t.Errorf("%v: expected error", args)
		}
	}
	if _, err := parseSettings([]string{"--config", "/nonexistent.yaml"}); err == nil {
		t.Error("missing config file should error")
	}
}

func TestBehaviorSettings(t *testing.T) {
	p := writeCfg(t, "failure_rate: 0.25\nack_delay: 500ms\nchurn_interval: 2m\nchurn_downtime: 5s\n")
	s, err := parseSettings([]string{"--config", p, "--failure-rate", "0.5"})
	if err != nil || s.FailureRate != 0.5 || s.AckDelay != 500*time.Millisecond || s.ChurnInterval != 2*time.Minute || s.ChurnDowntime != 5*time.Second {
		t.Fatalf("%v %+v", err, s)
	}
	for _, args := range [][]string{{"--failure-rate", "1.5"}, {"--ack-delay", "-1s"}} {
		if _, err := parseSettings(args); err == nil {
			t.Errorf("%v: expected error", args)
		}
	}
}
