// Command load-test is the scale/load harness for the device controller.
//
// It provisions and connects N simulated devices, lets them heartbeat for a
// measurement window, issues sample commands through the REST API to measure
// end-to-end command/ack latency, forces a subset to disconnect and reconnect,
// then writes a JSON + Markdown report to --report-dir. Every number in the
// report comes from a real run against a real controller + broker.
//
//	load-test --count 1000 --api-endpoint http://localhost:8000 \
//	    --mqtt-host localhost --admin-api-key dev-admin-key \
//	    --report-dir ../docs/load-test-results
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ramnindra/fleetmanagementplatform/simulator/internal/sim"
)

func percentile(values []float64, pct float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	s := append([]float64(nil), values...)
	sort.Float64s(s)
	v := s[min(len(s)-1, int(float64(len(s))*pct))]
	return &v
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

func pct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return round2(100 * float64(n) / float64(d))
}

func commandLatencies(api string, deviceIDs []string) []float64 {
	client := http.Client{Timeout: 10 * time.Second}
	var out []float64
	for _, id := range deviceIDs {
		start := time.Now()
		resp, err := client.Post(fmt.Sprintf("%s/api/v1/devices/%s/commands", api, id), "application/json",
			bytes.NewReader([]byte(`{"action":"ping"}`)))
		if err != nil {
			continue
		}
		var created struct {
			CommandID string `json:"command_id"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&created)
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			continue
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			r, err := client.Get(fmt.Sprintf("%s/api/v1/commands/%s", api, created.CommandID))
			if err == nil {
				var c struct {
					Status string `json:"status"`
				}
				_ = json.NewDecoder(r.Body).Decode(&c)
				r.Body.Close()
				if c.Status == "success" || c.Status == "failed" {
					out = append(out, time.Since(start).Seconds())
					break
				}
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	return out
}

func main() {
	count := flag.Int("count", 100, "number of simulated devices")
	deviceType := flag.String("device-type", "edge", "switch | gpu | edge")
	api := flag.String("api-endpoint", "http://localhost:8000", "controller REST endpoint")
	mqttHost := flag.String("mqtt-host", "localhost", "MQTT broker host")
	mqttPort := flag.Int("mqtt-port", 1883, "MQTT broker port")
	key := flag.String("admin-api-key", "dev-admin-key", "controller admin API key")
	hb := flag.Duration("heartbeat-interval", 30*time.Second, "heartbeat interval")
	connectWait := flag.Duration("connect-wait", 10*time.Second, "per-device connect wait before giving up")
	workers := flag.Int("provision-workers", 50, "concurrent provisioning workers")
	steady := flag.Duration("steady-state", 60*time.Second, "steady-state window")
	sampleCmds := flag.Int("sample-commands", 50, "how many devices to send a command to")
	reconnectSample := flag.Int("reconnect-sample", 20, "how many devices to force-disconnect")
	reportDir := flag.String("report-dir", "./load-test-results", "where to write the report")
	flag.Parse()

	cfg := sim.Config{
		APIEndpoint: *api, MQTTHost: *mqttHost, MQTTPort: *mqttPort, AdminAPIKey: *key,
		HeartbeatInterval: *hb, DeviceType: *deviceType, RunID: sim.NewRunID(), ConnectWait: *connectWait,
	}
	metrics := &sim.Metrics{}
	devices := make([]*sim.Device, *count)
	for i := range devices {
		devices[i] = sim.NewDevice(i, cfg.DeviceType, cfg, metrics)
	}
	report := map[string]any{
		"run_id": cfg.RunID, "started_at": time.Now().UTC().Format(time.RFC3339),
		"requested_device_count": *count, "heartbeat_interval_seconds": hb.Seconds(),
	}

	slog.Info("phase_provisioning_start", "count", *count)
	t0 := time.Now()
	connected := sim.ProvisionAll(devices, *workers)
	onboarding := map[string]any{
		"connected": len(connected), "requested": *count,
		"success_rate_percent": pct(len(connected), *count), "elapsed_seconds": round2(time.Since(t0).Seconds()),
	}
	report["onboarding"] = onboarding
	slog.Info("phase_provisioning_done", "onboarding", onboarding)
	if len(connected) == 0 {
		slog.Error("no_devices_connected_aborting")
		os.Exit(1)
	}

	stop := make(chan struct{})
	for _, d := range connected {
		go d.HeartbeatLoop(stop)
	}

	slog.Info("phase_steady_state", "seconds", steady.Seconds())
	warmup := min(10*time.Second, *steady) // let heartbeats establish before sampling
	time.Sleep(warmup)

	n := min(*sampleCmds, len(connected))
	ids := make([]string, n)
	for i := range ids {
		ids[i] = connected[i].ID
	}
	slog.Info("phase_command_latency_sample", "sample_size", n)
	lat := commandLatencies(*api, ids)
	var maxLat *float64
	for i := range lat {
		if maxLat == nil || lat[i] > *maxLat {
			maxLat = &lat[i]
		}
	}
	report["commands"] = map[string]any{
		"sampled": n, "succeeded": len(lat), "success_rate_percent": pct(len(lat), n),
		"ack_latency_seconds": map[string]any{"p50": percentile(lat, 0.50), "p95": percentile(lat, 0.95), "max": maxLat},
	}

	rs := connected[:min(*reconnectSample, len(connected))]
	slog.Info("phase_forced_reconnect", "sample_size", len(rs))
	t1 := time.Now()
	for _, d := range rs {
		d.Disconnect()
	}
	time.Sleep(2 * time.Second)
	for _, d := range rs {
		d.Connect()
	}
	report["forced_reconnect"] = map[string]any{"sample_size": len(rs), "elapsed_seconds": round2(time.Since(t1).Seconds())}

	if rest := *steady - warmup; rest > 0 {
		time.Sleep(rest)
	}
	close(stop)
	for _, d := range connected {
		d.Disconnect()
	}

	report["final_metrics"] = metrics.Snapshot()
	report["finished_at"] = time.Now().UTC().Format(time.RFC3339)

	if err := os.MkdirAll(*reportDir, 0o755); err != nil {
		slog.Error("report_dir", "error", err)
		os.Exit(1)
	}
	js, _ := json.MarshalIndent(report, "", "  ")
	jsonPath := filepath.Join(*reportDir, "load-test-"+cfg.RunID+".json")
	mdPath := filepath.Join(*reportDir, "load-test-"+cfg.RunID+".md")
	if err := os.WriteFile(jsonPath, js, 0o644); err != nil {
		slog.Error("write_report", "error", err)
		os.Exit(1)
	}
	if err := os.WriteFile(mdPath, []byte(renderMarkdown(report, string(js))), 0o644); err != nil {
		slog.Error("write_report", "error", err)
		os.Exit(1)
	}
	slog.Info("report_written", "json", jsonPath, "markdown", mdPath)
}

func renderMarkdown(r map[string]any, _ string) string {
	on := r["onboarding"].(map[string]any)
	cm := r["commands"].(map[string]any)
	lat := cm["ack_latency_seconds"].(map[string]any)
	rc := r["forced_reconnect"].(map[string]any)
	fm, _ := json.MarshalIndent(r["final_metrics"], "", "  ")
	val := func(v any) string {
		if p, ok := v.(*float64); ok {
			if p == nil {
				return "n/a"
			}
			return fmt.Sprintf("%.4f", *p)
		}
		return fmt.Sprint(v)
	}
	return fmt.Sprintf(`# Load Test Report — run %v

Started: %v
Finished: %v
Requested devices: %v
Heartbeat interval: %vs

## Onboarding
- Connected: %v / %v (%v%%)
- Elapsed: %vs

## Command latency (sample of %v)
- Success rate: %v%%
- p50 ack latency: %s
- p95 ack latency: %s
- max ack latency: %s

## Forced reconnect (sample of %v)
- Elapsed: %vs

## Final counters
`+"```json\n%s\n```"+`

These numbers are from one real run against a real controller + broker, not
projected or invented. Re-run with different --count to compare scale points
(10 / 100 / 1,000 per docs/roadmap.md Phase 8).
`, r["run_id"], r["started_at"], r["finished_at"], r["requested_device_count"], r["heartbeat_interval_seconds"],
		on["connected"], on["requested"], on["success_rate_percent"], on["elapsed_seconds"],
		cm["sampled"], cm["success_rate_percent"], val(lat["p50"]), val(lat["p95"]), val(lat["max"]),
		rc["sample_size"], rc["elapsed_seconds"], fm)
}
