// Package mqttc is the device agent's MQTT client.
package mqttc

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

type Client struct {
	deviceID    string
	c           paho.Client
	onCommand   func(map[string]any)
	onConnected func()
}

// onConnected runs on every (re)connect, so the agent can heartbeat immediately
// instead of waiting a full interval after a drop.
func New(deviceID, credential, host string, port int, onCommand func(map[string]any), onConnected func()) *Client {
	cl := &Client{deviceID: deviceID, onCommand: onCommand, onConnected: onConnected}
	opts := paho.NewClientOptions().
		AddBroker(fmt.Sprintf("tcp://%s:%d", host, port)).
		SetClientID(deviceID).
		SetUsername(deviceID).
		SetPassword(credential).
		SetKeepAlive(60 * time.Second).
		// Backoff capped at 60s so a reconnect storm across 1,000 devices
		// doesn't hammer the broker the instant it comes back up.
		SetAutoReconnect(true).
		SetMaxReconnectInterval(60 * time.Second).
		// ConnectRetry makes the *first* attempt retry too: a device that boots
		// before the network/controller is up must not crash, just wait.
		SetConnectRetry(true).
		SetConnectRetryInterval(2 * time.Second).
		SetOrderMatters(false).
		SetOnConnectHandler(cl.onConnect).
		SetConnectionLostHandler(func(_ paho.Client, err error) {
			slog.Warn("mqtt_disconnected", "device_id", deviceID, "error", err)
		})
	cl.c = paho.NewClient(opts)
	return cl
}

// Connect starts connecting in the background; it never blocks or fails.
func (cl *Client) Connect() { cl.c.Connect() }

func (cl *Client) Disconnect() { cl.c.Disconnect(500) }

func (cl *Client) IsConnected() bool { return cl.c.IsConnectionOpen() }

func (cl *Client) onConnect(c paho.Client) {
	slog.Info("mqtt_connected", "device_id", cl.deviceID)
	c.Subscribe(fmt.Sprintf("devices/%s/cmd", cl.deviceID), 1, cl.onMessage)
	if cl.onConnected != nil {
		go cl.onConnected()
	}
}

func (cl *Client) onMessage(_ paho.Client, msg paho.Message) {
	var payload map[string]any
	if err := json.Unmarshal(msg.Payload(), &payload); err != nil || payload == nil {
		slog.Warn("mqtt_bad_command_payload", "device_id", cl.deviceID)
		return
	}
	cl.onCommand(payload)
}

func (cl *Client) publish(suffix string, v any) {
	b, _ := json.Marshal(v)
	cl.c.Publish(fmt.Sprintf("devices/%s/%s", cl.deviceID, suffix), 1, false, b)
}

func (cl *Client) PublishHeartbeat(p map[string]any) { cl.publish("heartbeat", p) }
func (cl *Client) PublishTelemetry(p map[string]any) { cl.publish("telemetry", p) }

// PublishAck reports a command result. receivedAt is when the agent got the
// command, so the controller can show node-side receipt, not just broker delivery.
func (cl *Client) PublishAck(commandID, status string, result map[string]any, receivedAt time.Time) {
	cl.publish("cmd/ack", map[string]any{
		"command_id": commandID, "status": status, "result": result,
		"received_at": receivedAt.UTC().Format(time.RFC3339Nano),
	})
}
