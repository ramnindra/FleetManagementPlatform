// Command simulator spins up N simulated devices against a running controller
// and broker. For small smoke/integration runs (10-100 devices); for the
// 1,000-device scale test with a report, use load-test.
//
//	simulator --count 10 --api-endpoint http://localhost:8000 \
//	    --mqtt-host localhost --admin-api-key dev-admin-key
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ramnindra/fleetmanagementplatform/simulator/internal/sim"
)

func main() {
	count := flag.Int("count", 10, "number of simulated devices")
	deviceType := flag.String("device-type", "edge", "switch | gpu | edge")
	api := flag.String("api-endpoint", "http://localhost:8000", "controller REST endpoint")
	mqttHost := flag.String("mqtt-host", "localhost", "MQTT broker host")
	mqttPort := flag.Int("mqtt-port", 1883, "MQTT broker port")
	key := flag.String("admin-api-key", "dev-admin-key", "controller admin API key")
	hb := flag.Duration("heartbeat-interval", 10*time.Second, "heartbeat interval")
	duration := flag.Duration("duration", 60*time.Second, "run time; 0 = until Ctrl-C")
	flag.Parse()

	cfg := sim.Config{
		APIEndpoint: *api, MQTTHost: *mqttHost, MQTTPort: *mqttPort, AdminAPIKey: *key,
		HeartbeatInterval: *hb, DeviceType: *deviceType, RunID: sim.NewRunID(), ConnectWait: 10 * time.Second,
	}
	metrics := &sim.Metrics{}
	devices := make([]*sim.Device, *count)
	for i := range devices {
		devices[i] = sim.NewDevice(i, cfg, metrics)
	}

	slog.Info("provisioning_and_connecting", "count", *count)
	connected := sim.ProvisionAll(devices, 50)
	slog.Info("connected_summary", "connected", len(connected), "requested", *count)
	if len(connected) == 0 {
		slog.Error("no_devices_connected")
		os.Exit(1)
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	for _, d := range connected {
		go d.HeartbeatLoop(ctx.Done())
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-tick.C:
			slog.Info("metrics", "counters", metrics.Snapshot())
		}
	}
	for _, d := range connected {
		d.Disconnect()
	}
	slog.Info("final_metrics", "counters", metrics.Snapshot())
}
