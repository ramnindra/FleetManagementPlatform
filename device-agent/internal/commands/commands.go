// Package commands executes controller commands against an explicit allowlist.
//
// The controller can only ask the agent to run one of the handlers registered
// in allowed below. There is no code path from a command to a shell,
// subprocess, or eval: an unrecognized action is rejected, not interpreted
// (docs/architecture.md section 12).
package commands

import (
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime"
	"slices"
	"sort"
	"time"

	"github.com/ramnindra/fleetmanagementplatform/device-agent/internal/config"
	"github.com/ramnindra/fleetmanagementplatform/device-agent/internal/telemetry"
)

// updatableConfigKeys are the only keys an update_config command may change.
// Devices never let the controller rewrite arbitrary local state
// (e.g. credential_path, api_endpoint).
var updatableConfigKeys = []string{"heartbeat_interval_seconds"}

type handler func(cfg *config.Config, params map[string]any) (map[string]any, error)

var allowed = map[string]handler{
	"ping":            ping,
	"get_status":      getStatus,
	"get_system_info": getSystemInfo,
	"update_config":   updateConfig,
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

func ping(_ *config.Config, params map[string]any) (map[string]any, error) {
	return map[string]any{"pong": true, "echo": params["echo"], "uptime_seconds": round1(telemetry.UptimeSeconds())}, nil
}

func getStatus(cfg *config.Config, _ map[string]any) (map[string]any, error) {
	return map[string]any{
		"status": "healthy", "device_id": cfg.DeviceID, "device_type": cfg.DeviceType,
		"uptime_seconds": round1(telemetry.UptimeSeconds()),
	}, nil
}

func getSystemInfo(cfg *config.Config, _ map[string]any) (map[string]any, error) {
	host, _ := os.Hostname()
	return map[string]any{
		"hostname":    host,
		"platform":    runtime.GOOS + "/" + runtime.GOARCH,
		"go_version":  runtime.Version(),
		"device_type": cfg.DeviceType,
		"telemetry":   telemetry.Collect(cfg.DeviceType, cfg.SimulateGPU),
	}, nil
}

func updateConfig(cfg *config.Config, params map[string]any) (map[string]any, error) {
	applied := map[string]any{}
	rejected := []string{}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !slices.Contains(updatableConfigKeys, k) {
			rejected = append(rejected, k)
			continue
		}
		// k == heartbeat_interval_seconds (the only updatable key)
		n, ok := toInt(params[k])
		if !ok || n < 5 || n > 3600 {
			rejected = append(rejected, k)
			continue
		}
		cfg.SetHeartbeatIntervalSeconds(n)
		applied[k] = n
	}
	if len(applied) > 0 {
		slog.Info("config_updated", "applied", applied)
	}
	return map[string]any{"applied": applied, "rejected": rejected}, nil
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64: // JSON numbers
		if n != math.Trunc(n) {
			return 0, false
		}
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

// Execute returns ("success"|"failed", result). It never panics out of a handler.
func Execute(cfg *config.Config, action string, params map[string]any) (status string, result map[string]any) {
	h, ok := allowed[action]
	if !ok {
		slog.Warn("command_rejected_not_allowlisted", "action", action)
		return "failed", map[string]any{"error": fmt.Sprintf("action '%s' is not in the allowlist", action)}
	}
	if params == nil {
		params = map[string]any{}
	}
	started := time.Now()
	defer func() {
		if r := recover(); r != nil { // a bad command must never crash the agent
			slog.Error("command_execution_panicked", "action", action, "panic", r)
			status, result = "failed", map[string]any{"error": fmt.Sprint(r)}
		}
	}()
	res, err := h(cfg, params)
	if err != nil {
		slog.Error("command_execution_failed", "action", action, "error", err)
		return "failed", map[string]any{"error": err.Error()}
	}
	res["duration_ms"] = round1(float64(time.Since(started).Microseconds()) / 1000)
	return "success", res
}
