// Package provision registers devices from an inventory with the controller
// and renders per-device enrollment bundles.
//
// This is the primary provisioning/delivery method for this reference
// implementation (see docs/provisioning.md): an operator copies the rendered
// bundle onto the device and starts the agent. The agent performs the actual
// enrollment call using the one-time token in the bundle — this package never
// talks MQTT and never holds a device's long-lived credential.
//
// Adding, removing, or changing a device is purely an inventory-file + rerun
// operation; no controller source change is needed.
package provision

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed *.tmpl
var templates embed.FS

type InventoryDevice struct {
	DeviceID                 string         `yaml:"device_id"`
	DeviceType               string         `yaml:"device_type"`
	HeartbeatIntervalSeconds *int           `yaml:"heartbeat_interval_seconds"`
	Attributes               map[string]any `yaml:"attributes"`
}

func LoadInventory(path string) ([]InventoryDevice, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var inv struct {
		Devices []InventoryDevice `yaml:"devices"`
	}
	if err := yaml.Unmarshal(raw, &inv); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i, d := range inv.Devices {
		if d.DeviceID == "" || d.DeviceType == "" {
			return nil, fmt.Errorf("%s: devices[%d] needs device_id and device_type", path, i)
		}
	}
	return inv.Devices, nil
}

// BundleData is the template context for config.yaml and README.txt.
type BundleData struct {
	DeviceID                 string
	DeviceType               string
	APIEndpoint              string
	MQTTHost                 string
	MQTTPort                 int
	HeartbeatIntervalSeconds int
	EnrollmentToken          string
	SimulateGPU              bool
}

// RenderBundle writes <outputDir>/<device_id>/{config.yaml,README.txt}.
// templateOverride, if non-empty, is a custom config.yaml template file.
func RenderBundle(outputDir, templateOverride string, data BundleData) (string, error) {
	var cfgTmpl *template.Template
	var err error
	if templateOverride != "" {
		cfgTmpl, err = template.ParseFiles(templateOverride)
	} else {
		cfgTmpl, err = template.ParseFS(templates, "device-config.yaml.tmpl")
	}
	if err != nil {
		return "", err
	}
	readmeTmpl, err := template.ParseFS(templates, "README.txt.tmpl")
	if err != nil {
		return "", err
	}

	dir := filepath.Join(outputDir, data.DeviceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	render := func(t *template.Template, name string, mode os.FileMode) error {
		var buf bytes.Buffer
		if err := t.Execute(&buf, data); err != nil {
			return err
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, buf.Bytes(), mode); err != nil {
			return err
		}
		return os.Chmod(p, mode) // WriteFile doesn't narrow an existing file's mode
	}
	// config.yaml holds a one-time enrollment token: owner-only.
	if err := render(cfgTmpl, "config.yaml", 0o600); err != nil {
		return "", err
	}
	if err := render(readmeTmpl, "README.txt", 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// Client talks to the controller's REST API.
type Client struct {
	APIEndpoint string
	AdminAPIKey string
	HTTP        *http.Client
}

func NewClient(endpoint, key string) *Client {
	return &Client{APIEndpoint: endpoint, AdminAPIKey: key, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) IsRegistered(deviceID string) (bool, error) {
	resp, err := c.HTTP.Get(fmt.Sprintf("%s/api/v1/devices/%s", c.APIEndpoint, deviceID))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("checking %s: HTTP %d", deviceID, resp.StatusCode)
}

// Register returns the one-time enrollment token.
func (c *Client) Register(d InventoryDevice) (string, error) {
	attrs := d.Attributes
	if attrs == nil {
		attrs = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{"device_id": d.DeviceID, "device_type": d.DeviceType, "attributes": attrs})
	req, _ := http.NewRequest(http.MethodPost, c.APIEndpoint+"/api/v1/devices/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", c.AdminAPIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("register %s: HTTP %d: %s", d.DeviceID, resp.StatusCode, raw)
	}
	var out struct {
		EnrollmentToken string `json:"enrollment_token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.EnrollmentToken == "" {
		return "", fmt.Errorf("register %s: response missing enrollment_token", d.DeviceID)
	}
	return out.EnrollmentToken, nil
}

func (c *Client) IsOnline(deviceID string) bool {
	resp, err := c.HTTP.Get(fmt.Sprintf("%s/api/v1/devices/%s/status", c.APIEndpoint, deviceID))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		Online bool `json:"online"`
	}
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&out) == nil && out.Online
}
