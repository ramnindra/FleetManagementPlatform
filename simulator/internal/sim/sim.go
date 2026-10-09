// Package sim holds the simulated-device logic shared by cmd/simulator (smoke
// tests with a handful of devices) and cmd/load-test (hundreds to 1,000
// devices). A simulated device goes through the exact same register -> enroll
// -> connect -> heartbeat -> command/ack flow as the real device-agent, just
// driven by goroutines instead of one process per device, so it is an honest
// exercise of the controller and broker.
package sim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// Metrics are counters shared across all simulated devices in a run.
type Metrics struct {
	DevicesProvisioned     atomic.Int64
	DevicesProvisionFailed atomic.Int64
	DevicesConnected       atomic.Int64
	DevicesConnectFailed   atomic.Int64
	HeartbeatsSent         atomic.Int64
	CommandsReceived       atomic.Int64
	CommandsAcked          atomic.Int64
	Reconnects             atomic.Int64
	InjectedFailures       atomic.Int64
	ChurnDrops             atomic.Int64
}

func (m *Metrics) Snapshot() map[string]int64 {
	return map[string]int64{
		"devices_provisioned":      m.DevicesProvisioned.Load(),
		"devices_provision_failed": m.DevicesProvisionFailed.Load(),
		"devices_connected":        m.DevicesConnected.Load(),
		"devices_connect_failed":   m.DevicesConnectFailed.Load(),
		"heartbeats_sent":          m.HeartbeatsSent.Load(),
		"commands_received":        m.CommandsReceived.Load(),
		"commands_acked":           m.CommandsAcked.Load(),
		"reconnects":               m.Reconnects.Load(),
		"injected_failures":        m.InjectedFailures.Load(),
		"churn_drops":              m.ChurnDrops.Load(),
	}
}

type Config struct {
	APIEndpoint       string
	MQTTHost          string
	MQTTPort          int
	AdminAPIKey       string
	HeartbeatInterval time.Duration
	DeviceType        string // default type for devices created with NewDevice callers that pass none
	RunID             string
	// ConnectWait is how long each device waits for its own connect callback
	// before giving up. At 1,000 devices this also measures the simulator's
	// own scheduling, not just the broker/controller (docs/load-test-report.md).
	ConnectWait time.Duration

	// Behavior knobs (all off by default).
	FailureRate   float64       // fraction of commands (0..1) a device fails instead of executing
	AckDelay      time.Duration // each ack is delayed by a random 0..AckDelay
	ChurnInterval time.Duration // mean time between random drops per device; 0 = never
	ChurnDowntime time.Duration // how long a dropped device stays offline

	// OnCommand, if set, is called for every instruction a device receives
	// from the controller (e.g. sent from the web UI) after it has been handled.
	OnCommand func(CommandEvent)
}

// CommandEvent describes one instruction received by a simulated device.
type CommandEvent struct {
	DeviceID  string
	CommandID string
	Action    string
	Params    map[string]any
	Status    string
	Result    map[string]any
}

func NewRunID() string { return fmt.Sprintf("%d-%d", time.Now().Unix(), 1000+rand.IntN(9000)) }

var httpClient = &http.Client{Timeout: 10 * time.Second}

type Device struct {
	ID         string
	Type       string
	interval   atomic.Int64 // heartbeat interval in nanoseconds; update_config can change it
	cfg        Config
	metrics    *Metrics
	credential string

	mu        sync.Mutex
	client    paho.Client
	connected chan struct{} // closed when the current connection attempt succeeds
	expected  bool          // next disconnect is intentional (not a "reconnect")
	up        bool
}

func NewDevice(index int, deviceType string, cfg Config, m *Metrics) *Device {
	d := &Device{ID: fmt.Sprintf("%s-sim-%s-%05d", deviceType, cfg.RunID, index), Type: deviceType, cfg: cfg, metrics: m}
	d.interval.Store(int64(cfg.HeartbeatInterval))
	return d
}

func postJSON(url string, headers map[string]string, body any, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s: HTTP %d: %s", url, resp.StatusCode, raw)
	}
	return json.Unmarshal(raw, out)
}

// Provision registers and enrolls the device, storing its credential.
func (d *Device) Provision() bool {
	var reg struct {
		EnrollmentToken string `json:"enrollment_token"`
	}
	err := postJSON(d.cfg.APIEndpoint+"/api/v1/devices/register",
		map[string]string{"X-Api-Key": d.cfg.AdminAPIKey},
		map[string]string{"device_id": d.ID, "device_type": d.Type}, &reg)
	var enr struct {
		DeviceCredential string `json:"device_credential"`
	}
	if err == nil {
		err = postJSON(fmt.Sprintf("%s/api/v1/devices/%s/enroll", d.cfg.APIEndpoint, d.ID), nil,
			map[string]string{"enrollment_token": reg.EnrollmentToken}, &enr)
	}
	if err != nil {
		slog.Warn("provision_failed", "device_id", d.ID, "error", err)
		d.metrics.DevicesProvisionFailed.Add(1)
		return false
	}
	d.credential = enr.DeviceCredential
	d.metrics.DevicesProvisioned.Add(1)
	return true
}

// Connect opens the MQTT connection and waits up to ConnectWait for success.
func (d *Device) Connect() bool {
	connected := make(chan struct{})
	var once sync.Once
	opts := paho.NewClientOptions().
		AddBroker(fmt.Sprintf("tcp://%s:%d", d.cfg.MQTTHost, d.cfg.MQTTPort)).
		SetClientID(d.ID).SetUsername(d.ID).SetPassword(d.credential).
		SetKeepAlive(30 * time.Second).
		SetAutoReconnect(true).SetMaxReconnectInterval(30 * time.Second).
		SetOrderMatters(false).
		// Only a *successful* connect calls this handler (paho does not invoke
		// it for rejected auth), so devices_connected stays trustworthy.
		SetOnConnectHandler(func(c paho.Client) {
			c.Subscribe(fmt.Sprintf("devices/%s/cmd", d.ID), 1, d.onMessage)
			d.mu.Lock()
			d.up = true
			d.mu.Unlock()
			once.Do(func() { close(connected) })
		}).
		SetConnectionLostHandler(func(paho.Client, error) {
			// Only unplanned drops count as "reconnects"; an intentional
			// Disconnect() (forced-reconnect test, shutdown) sets expected first.
			d.mu.Lock()
			if d.up && !d.expected {
				d.metrics.Reconnects.Add(1)
			}
			d.up, d.expected = false, false
			d.mu.Unlock()
		})
	c := paho.NewClient(opts)
	d.mu.Lock()
	d.client, d.connected = c, connected
	d.mu.Unlock()

	// Non-blocking Connect: retries in the background; we bound the wait.
	t := c.Connect()
	if t.WaitTimeout(d.cfg.ConnectWait) && t.Error() == nil {
		d.metrics.DevicesConnected.Add(1)
		return true
	}
	// Without this a client that never connected would keep retrying forever,
	// hammering EMQX's auth webhook for the rest of the test (the zombie
	// clients that distorted an earlier 1,000-device run; see
	// docs/load-test-report.md).
	c.Disconnect(0)
	d.metrics.DevicesConnectFailed.Add(1)
	return false
}

func (d *Device) onMessage(c paho.Client, msg paho.Message) {
	var payload map[string]any
	if json.Unmarshal(msg.Payload(), &payload) != nil {
		return
	}
	d.metrics.CommandsReceived.Add(1)
	commandID, _ := payload["command_id"].(string)
	action, _ := payload["action"].(string)
	params, _ := payload["params"].(map[string]any)

	if d.cfg.AckDelay > 0 {
		time.Sleep(time.Duration(rand.Float64() * float64(d.cfg.AckDelay)))
	}
	var status string
	var result map[string]any
	if d.cfg.FailureRate > 0 && rand.Float64() < d.cfg.FailureRate {
		d.metrics.InjectedFailures.Add(1)
		status, result = "failed", map[string]any{"error": "simulated failure (injected)", "simulated": true}
	} else {
		status, result = d.execute(action, params)
	}
	ack, _ := json.Marshal(map[string]any{"command_id": commandID, "status": status, "result": result})
	c.Publish(fmt.Sprintf("devices/%s/cmd/ack", d.ID), 1, false, ack)
	d.metrics.CommandsAcked.Add(1)

	if d.cfg.OnCommand != nil {
		d.cfg.OnCommand(CommandEvent{d.ID, commandID, action, params, status, result})
	}
}

// execute mirrors the real device-agent's allowlist, with simulated data.
func (d *Device) execute(action string, params map[string]any) (string, map[string]any) {
	switch action {
	case "ping":
		return "success", map[string]any{"pong": true, "echo": params["echo"], "simulated": true}
	case "get_status":
		return "success", map[string]any{"status": "healthy", "device_id": d.ID, "device_type": d.Type, "simulated": true}
	case "get_system_info":
		return "success", map[string]any{"hostname": d.ID, "platform": "simulated", "device_type": d.Type, "telemetry": Telemetry(d.Type), "simulated": true}
	case "update_config":
		applied, rejected := map[string]any{}, []string{}
		for k, v := range params {
			n, ok := v.(float64)
			if k != "heartbeat_interval_seconds" || !ok || n != float64(int(n)) || n < 5 || n > 3600 {
				rejected = append(rejected, k)
				continue
			}
			d.interval.Store(int64(time.Duration(n) * time.Second))
			applied[k] = int(n)
		}
		return "success", map[string]any{"applied": applied, "rejected": rejected, "simulated": true}
	}
	return "failed", map[string]any{"error": fmt.Sprintf("action '%s' is not in the allowlist", action), "simulated": true}
}

func (d *Device) SendHeartbeat() {
	d.mu.Lock()
	c := d.client
	d.mu.Unlock()
	if c == nil || !c.IsConnectionOpen() {
		return
	}
	hb, _ := json.Marshal(map[string]any{"status": "healthy", "ts": float64(time.Now().UnixMilli()) / 1000})
	c.Publish(fmt.Sprintf("devices/%s/heartbeat", d.ID), 1, false, hb)
	tel, _ := json.Marshal(Telemetry(d.Type))
	c.Publish(fmt.Sprintf("devices/%s/telemetry", d.ID), 1, false, tel)
	d.metrics.HeartbeatsSent.Add(1)
}

func (d *Device) Disconnect() {
	d.mu.Lock()
	c := d.client
	d.expected = true
	d.mu.Unlock()
	if c != nil {
		c.Disconnect(250)
	}
}

func (d *Device) HeartbeatInterval() time.Duration { return time.Duration(d.interval.Load()) }

// HeartbeatLoop runs until stop is closed. The interval is re-read each cycle
// so an update_config instruction takes effect immediately.
func (d *Device) HeartbeatLoop(stop <-chan struct{}) {
	// Jitter avoids every device heartbeating in the same instant.
	select {
	case <-time.After(time.Duration(rand.Float64() * float64(d.HeartbeatInterval()))):
	case <-stop:
		return
	}
	for {
		d.SendHeartbeat()
		select {
		case <-time.After(d.HeartbeatInterval()):
		case <-stop:
			return
		}
	}
}

// Reconnect re-establishes the MQTT connection after an intentional drop.
func (d *Device) Reconnect() bool {
	before := d.metrics.DevicesConnected.Load()
	ok := d.Connect()
	if ok {
		d.metrics.DevicesConnected.Store(before) // not a new device
	}
	return ok
}

// ChurnLoop randomly drops this device off the network and brings it back, to
// exercise the controller's offline detection and the broker's reconnect path.
// Intervals are exponentially distributed around ChurnInterval.
func (d *Device) ChurnLoop(stop <-chan struct{}) {
	if d.cfg.ChurnInterval <= 0 {
		return
	}
	for {
		wait := time.Duration(rand.ExpFloat64() * float64(d.cfg.ChurnInterval))
		select {
		case <-time.After(wait):
		case <-stop:
			return
		}
		d.Disconnect()
		d.metrics.ChurnDrops.Add(1)
		select {
		case <-time.After(d.cfg.ChurnDowntime):
		case <-stop:
			return
		}
		for !d.Reconnect() {
			select {
			case <-time.After(2 * time.Second):
			case <-stop:
				return
			}
		}
	}
}

func (d *Device) ProvisionAndConnect() bool { return d.Provision() && d.Connect() }

// ProvisionAll runs ProvisionAndConnect for all devices with a bounded worker
// pool and returns the devices that connected.
func ProvisionAll(devices []*Device, workers int) []*Device {
	results := make([]bool, len(devices))
	sem := make(chan struct{}, max(1, workers))
	var wg sync.WaitGroup
	for i, d := range devices {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = d.ProvisionAndConnect()
		}()
	}
	wg.Wait()
	var ok []*Device
	for i, d := range devices {
		if results[i] {
			ok = append(ok, d)
		}
	}
	return ok
}
