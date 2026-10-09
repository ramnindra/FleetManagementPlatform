// Command device-agent runs on a managed device: it enrolls with the
// controller, then heartbeats and executes allowlisted commands over MQTT.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ramnindra/fleetmanagementplatform/device-agent/internal/commands"
	"github.com/ramnindra/fleetmanagementplatform/device-agent/internal/config"
	"github.com/ramnindra/fleetmanagementplatform/device-agent/internal/enrollment"
	"github.com/ramnindra/fleetmanagementplatform/device-agent/internal/mqttc"
	"github.com/ramnindra/fleetmanagementplatform/device-agent/internal/telemetry"
)

type agent struct {
	cfg  *config.Config
	mqtt *mqttc.Client
}

func (a *agent) ensureCredential() (string, error) {
	cred, err := config.LoadCredential(a.cfg.CredentialPath)
	if err != nil {
		return "", err
	}
	if cred != "" {
		slog.Info("using_existing_credential", "device_id", a.cfg.DeviceID)
		return cred, nil
	}
	if a.cfg.EnrollmentToken == "" {
		return "", fmt.Errorf("no stored credential and no enrollment token in config — " +
			"this device has not been provisioned (see docs/provisioning.md)")
	}
	slog.Info("enrolling_device", "device_id", a.cfg.DeviceID)
	cred, err = enrollment.Enroll(a.cfg.APIEndpoint, a.cfg.DeviceID, a.cfg.EnrollmentToken)
	if err != nil {
		return "", err
	}
	if err := config.SaveCredential(a.cfg.CredentialPath, cred); err != nil {
		return "", err
	}
	return cred, nil
}

func (a *agent) handleCommand(payload map[string]any) {
	receivedAt := time.Now()
	commandID, _ := payload["command_id"].(string)
	action, _ := payload["action"].(string)
	if commandID == "" || action == "" {
		slog.Warn("malformed_command_payload", "payload", payload)
		return
	}
	params, _ := payload["params"].(map[string]any)
	slog.Info("command_received", "command_id", commandID, "action", action)
	status, result := commands.Execute(a.cfg, action, params)
	a.mqtt.PublishAck(commandID, status, result, receivedAt)
	slog.Info("command_acked", "command_id", commandID, "status", status)
}

func (a *agent) sendHeartbeat() {
	a.mqtt.PublishHeartbeat(map[string]any{"status": "healthy", "ts": float64(time.Now().UnixMilli()) / 1000})
	a.mqtt.PublishTelemetry(telemetry.Collect(a.cfg.DeviceType, a.cfg.SimulateGPU))
}

func (a *agent) run() error {
	cred, err := a.ensureCredential()
	if err != nil {
		return err
	}
	a.mqtt = mqttc.New(a.cfg.DeviceID, cred, a.cfg.MQTTHost, a.cfg.MQTTPort, a.handleCommand, a.sendHeartbeat)
	a.mqtt.Connect()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	slog.Info("agent_started", "device_id", a.cfg.DeviceID)
	for {
		a.sendHeartbeat()
		select {
		case <-stop:
			a.mqtt.Disconnect()
			slog.Info("agent_stopped", "device_id", a.cfg.DeviceID)
			return nil
		// Re-read each cycle so update_config takes effect without a restart.
		case <-time.After(time.Duration(a.cfg.HeartbeatIntervalSeconds()) * time.Second):
		}
	}
}

func main() {
	path := os.Getenv("AGENT_CONFIG_PATH")
	if path == "" {
		path = "./config.yaml"
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "failed to load config:", err)
		os.Exit(1)
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToUpper(cfg.LogLevel))); err != nil {
		level = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	if err := (&agent{cfg: cfg}).run(); err != nil {
		slog.Error("agent_failed", "error", err)
		os.Exit(1)
	}
}
