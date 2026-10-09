// Command bulk-provision reads an inventory file (see
// provisioning/inventory/devices.example.yaml), registers each new device with
// the controller, and renders a per-device enrollment bundle under
// --output-dir/<device_id>/.
//
//	bulk-provision --inventory inventory/devices.example.yaml \
//	    --api-endpoint http://localhost:8000 --admin-api-key dev-admin-key \
//	    --mqtt-host localhost --output-dir output
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ramnindra/fleetmanagementplatform/provisioning/internal/provision"
)

func main() {
	inventory := flag.String("inventory", "", "inventory YAML file (required)")
	tmpl := flag.String("template", "", "custom config.yaml template (default: built-in)")
	outDir := flag.String("output-dir", "output", "where bundles are written")
	api := flag.String("api-endpoint", "http://localhost:8000", "controller REST endpoint")
	mqttHost := flag.String("mqtt-host", "localhost", "MQTT broker host written into bundles")
	mqttPort := flag.Int("mqtt-port", 1883, "MQTT broker port written into bundles")
	key := flag.String("admin-api-key", "", "controller admin API key (required)")
	defHB := flag.Int("default-heartbeat-interval", 30, "heartbeat seconds when the inventory omits it")
	simGPU := flag.Bool("simulate-gpu", true, "set telemetry.simulate_gpu in bundles")
	verify := flag.Bool("verify", false, "poll device status after provisioning")
	verifyTimeout := flag.Duration("verify-timeout", 30*time.Second, "how long --verify waits")
	flag.Parse()

	if *inventory == "" || *key == "" {
		fmt.Fprintln(os.Stderr, "--inventory and --admin-api-key are required")
		flag.Usage()
		os.Exit(2)
	}
	devices, err := provision.LoadInventory(*inventory)
	if err != nil {
		slog.Error("load_inventory", "error", err)
		os.Exit(1)
	}
	client := provision.NewClient(*api, *key)

	var provisioned []string
	skipped, failed := 0, 0
	for _, d := range devices {
		registered, err := client.IsRegistered(d.DeviceID)
		if err != nil {
			slog.Error("provisioning_failed", "device_id", d.DeviceID, "error", err)
			failed++
			continue
		}
		if registered {
			slog.Info("already_registered_skipping", "device_id", d.DeviceID)
			skipped++
			continue
		}
		token, err := client.Register(d)
		if err != nil {
			slog.Error("provisioning_failed", "device_id", d.DeviceID, "error", err)
			failed++
			continue
		}
		hb := *defHB
		if d.HeartbeatIntervalSeconds != nil {
			hb = *d.HeartbeatIntervalSeconds
		}
		dir, err := provision.RenderBundle(*outDir, *tmpl, provision.BundleData{
			DeviceID: d.DeviceID, DeviceType: d.DeviceType, APIEndpoint: *api, MQTTHost: *mqttHost, MQTTPort: *mqttPort,
			HeartbeatIntervalSeconds: hb, EnrollmentToken: token, SimulateGPU: *simGPU,
		})
		if err != nil {
			slog.Error("render_bundle_failed", "device_id", d.DeviceID, "error", err)
			failed++
			continue
		}
		slog.Info("provisioned", "device_id", d.DeviceID, "bundle", filepath.Clean(dir))
		provisioned = append(provisioned, d.DeviceID)
	}

	slog.Info("summary", "provisioned", len(provisioned), "skipped", skipped, "failed", failed, "total", len(devices))
	fmt.Printf("provisioned=%d skipped=%d failed=%d total=%d\n", len(provisioned), skipped, failed, len(devices))

	if *verify && len(provisioned) > 0 {
		verifyOnboarding(client, provisioned, *verifyTimeout)
	}
	if failed > 0 {
		os.Exit(1)
	}
}

func verifyOnboarding(c *provision.Client, ids []string, timeout time.Duration) {
	slog.Info("verifying_onboarding", "count", len(ids), "timeout", timeout)
	pending := slices.Clone(ids)
	deadline := time.Now().Add(timeout)
	for len(pending) > 0 && time.Now().Before(deadline) {
		pending = slices.DeleteFunc(pending, c.IsOnline)
		if len(pending) > 0 {
			time.Sleep(2 * time.Second)
		}
	}
	fmt.Printf("onboarding_verified: %d/%d devices online within %s\n", len(ids)-len(pending), len(ids), timeout)
	if len(pending) > 0 {
		fmt.Printf("still offline (agent not started yet?): %v\n", pending)
	}
}
