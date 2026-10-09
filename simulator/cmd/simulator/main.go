// Command simulator connects N simulated devices ("nodes") to a running
// controller. Each one registers, enrolls, connects over MQTT, heartbeats, and
// executes the instructions it receives (for example, commands sent from the
// controller's web UI), printing every instruction as it arrives.
//
// Parameters come from flags, a YAML config file (--config), or both; flags
// override the file.
//
//	simulator --cluster localhost --count 50
//	simulator --config simulator.example.yaml --count 5
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ramnindra/fleetmanagementplatform/simulator/internal/sim"
)

func main() {
	s, err := parseSettings(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}

	var printMu sync.Mutex
	cfg := sim.Config{
		APIEndpoint: s.APIEndpoint, MQTTHost: s.MQTTHost, MQTTPort: s.MQTTPort, AdminAPIKey: s.AdminAPIKey,
		HeartbeatInterval: s.HeartbeatInterval, DeviceType: s.DeviceType, RunID: sim.NewRunID(), ConnectWait: s.ConnectWait,
		OnCommand: func(e sim.CommandEvent) {
			params, _ := json.Marshal(e.Params)
			result, _ := json.Marshal(e.Result)
			printMu.Lock()
			defer printMu.Unlock()
			fmt.Printf("%s  ← INSTRUCTION  %-34s %-16s params=%s\n", time.Now().Format("15:04:05"), e.DeviceID, e.Action, params)
			fmt.Printf("%s  → %-8s     %-34s %s\n", time.Now().Format("15:04:05"), e.Status, e.DeviceID, trunc(string(result), 110))
		},
	}

	metrics := &sim.Metrics{}
	types := s.Types()
	devices := make([]*sim.Device, len(types))
	for i, t := range types {
		devices[i] = sim.NewDevice(i, t, cfg, metrics)
	}

	fmt.Printf("simulator: %d devices -> controller %s, broker %s:%d (run %s)\n",
		len(devices), s.APIEndpoint, s.MQTTHost, s.MQTTPort, cfg.RunID)
	connected := sim.ProvisionAll(devices, s.ProvisionWorkers)
	fmt.Printf("simulator: %d/%d devices connected — they should now appear in the web UI at %s/ui/\n",
		len(connected), len(devices), s.APIEndpoint)
	if len(connected) == 0 {
		fmt.Fprintln(os.Stderr, "no devices connected; is the controller reachable and the admin key correct?")
		os.Exit(1)
	}
	if len(connected) <= 20 {
		for _, d := range connected {
			fmt.Printf("  - %s (%s)\n", d.ID, d.Type)
		}
	}
	fmt.Println("simulator: waiting for instructions from the web UI (Ctrl-C to stop)")

	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()
	if s.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.Duration)
		defer cancel()
	}

	for _, d := range connected {
		go d.HeartbeatLoop(ctx.Done())
	}
	tick := time.NewTicker(15 * time.Second)
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
	fmt.Println("simulator: stopped.", strings.TrimSpace(fmt.Sprint(metrics.Snapshot())))
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
