// Package config loads the device agent's YAML config and persists its credential.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DeviceID        string
	DeviceType      string
	APIEndpoint     string
	MQTTHost        string
	MQTTPort        int
	LogLevel        string
	CredentialPath  string
	EnrollmentToken string // only present before first successful enrollment
	SimulateGPU     bool

	mu                       sync.RWMutex
	heartbeatIntervalSeconds int
}

func (c *Config) HeartbeatIntervalSeconds() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.heartbeatIntervalSeconds
}

func (c *Config) SetHeartbeatIntervalSeconds(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.heartbeatIntervalSeconds = n
}

type file struct {
	Device struct {
		ID   string `yaml:"id"`
		Type string `yaml:"type"`
	} `yaml:"device"`
	Controller struct {
		APIEndpoint string `yaml:"api_endpoint"`
		MQTTHost    string `yaml:"mqtt_host"`
		MQTTPort    *int   `yaml:"mqtt_port"`
	} `yaml:"controller"`
	Heartbeat struct {
		IntervalSeconds *int `yaml:"interval_seconds"`
	} `yaml:"heartbeat"`
	Logging struct {
		Level string `yaml:"level"`
	} `yaml:"logging"`
	Agent struct {
		CredentialPath string `yaml:"credential_path"`
	} `yaml:"agent"`
	Enrollment struct {
		Token string `yaml:"token"`
	} `yaml:"enrollment"`
	Telemetry struct {
		SimulateGPU *bool `yaml:"simulate_gpu"`
	} `yaml:"telemetry"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f file
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, req := range []struct{ name, val string }{
		{"device.id", f.Device.ID},
		{"device.type", f.Device.Type},
		{"controller.api_endpoint", f.Controller.APIEndpoint},
		{"controller.mqtt_host", f.Controller.MQTTHost},
	} {
		if req.val == "" {
			return nil, fmt.Errorf("missing required config field: '%s'", req.name)
		}
	}

	c := &Config{
		DeviceID: f.Device.ID, DeviceType: f.Device.Type,
		APIEndpoint: f.Controller.APIEndpoint, MQTTHost: f.Controller.MQTTHost,
		MQTTPort: 1883, heartbeatIntervalSeconds: 30, LogLevel: "INFO", SimulateGPU: true,
		CredentialPath:  "/etc/device-agent/" + f.Device.ID + ".credential",
		EnrollmentToken: f.Enrollment.Token,
	}
	if f.Controller.MQTTPort != nil {
		c.MQTTPort = *f.Controller.MQTTPort
	}
	if f.Heartbeat.IntervalSeconds != nil {
		c.heartbeatIntervalSeconds = *f.Heartbeat.IntervalSeconds
	}
	if f.Logging.Level != "" {
		c.LogLevel = f.Logging.Level
	}
	if f.Agent.CredentialPath != "" {
		c.CredentialPath = f.Agent.CredentialPath
	}
	if f.Telemetry.SimulateGPU != nil {
		c.SimulateGPU = *f.Telemetry.SimulateGPU
	}
	return c, nil
}

// LoadCredential returns "" (and no error) if no credential has been stored yet.
func LoadCredential(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

// SaveCredential writes the credential owner-read/write only (0600).
func SaveCredential(path, credential string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(credential), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600) // WriteFile doesn't narrow an existing file's mode
}
