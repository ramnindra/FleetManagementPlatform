// Package mqttc is the controller's MQTT client: it consumes device
// heartbeats/telemetry/acks and publishes commands. Any controller replica can
// publish for any device; the broker owns routing, which is what lets us run
// multiple stateless replicas without duplicate execution.
package mqttc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/ramnindra/fleetmanagementplatform/controller/internal/config"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/metrics"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/service"
)

const (
	heartbeatTopic = "devices/+/heartbeat"
	telemetryTopic = "devices/+/telemetry"
	ackTopic       = "devices/+/cmd/ack"
)

type Client struct {
	c   paho.Client
	svc *service.Service
	m   *metrics.Metrics
}

func New(cfg config.Settings, svc *service.Service, m *metrics.Metrics) *Client {
	cl := &Client{svc: svc, m: m}
	host, _ := os.Hostname()
	opts := paho.NewClientOptions().
		AddBroker(fmt.Sprintf("tcp://%s:%d", cfg.MQTTHost, cfg.MQTTPort)).
		SetClientID(fmt.Sprintf("controller-%s-%d", host, os.Getpid())).
		SetUsername(cfg.MQTTClientUsername).
		SetPassword(cfg.MQTTClientPassword).
		SetKeepAlive(time.Duration(cfg.MQTTKeepaliveSecond) * time.Second).
		// ConnectRetry + non-blocking Connect: a broker that isn't up yet (e.g.
		// StatefulSet pod still starting on a fresh deploy) is a transient
		// retry, not a startup crash.
		SetConnectRetry(true).
		SetConnectRetryInterval(2 * time.Second).
		SetAutoReconnect(true).
		SetMaxReconnectInterval(30 * time.Second).
		SetOrderMatters(false).
		SetOnConnectHandler(cl.onConnect).
		SetConnectionLostHandler(func(_ paho.Client, err error) { slog.Warn("mqtt_disconnected", "error", err) })
	cl.c = paho.NewClient(opts)
	return cl
}

// Start begins connecting in the background; it never blocks or fails.
func (cl *Client) Start() { cl.c.Connect() }

func (cl *Client) Stop() { cl.c.Disconnect(500) }

func (cl *Client) onConnect(c paho.Client) {
	slog.Info("mqtt_connected")
	// Subscribe from a goroutine: blocking inside the connect handler would stall paho.
	go cl.subscribeAll(c)
}

// subscribeAll subscribes to every device topic and retries any the broker
// refuses. A SUBACK can grant a topic with failure code 0x80 — notably when
// EMQX's authz webhook (served by this very controller) is not reachable yet
// on a fresh deploy. Without a retry that subscription would be silently lost
// until the next reconnect, and e.g. heartbeats would never be recorded.
func (cl *Client) subscribeAll(c paho.Client) {
	pending := map[string]byte{heartbeatTopic: 1, telemetryTopic: 1, ackTopic: 1}
	for delay := time.Second; len(pending) > 0 && c.IsConnectionOpen(); delay = min(delay*2, 15*time.Second) {
		t := c.SubscribeMultiple(pending, cl.onMessage)
		t.Wait()
		if st, ok := t.(*paho.SubscribeToken); ok && t.Error() == nil {
			for topic, code := range st.Result() {
				if code != 0x80 {
					delete(pending, topic)
				}
			}
		}
		if len(pending) == 0 {
			slog.Info("mqtt_subscribed")
			return
		}
		slog.Warn("mqtt_subscribe_refused_retrying", "topics", len(pending), "retry_in", delay)
		time.Sleep(delay)
	}
}

// parseReceivedAt reads the device-reported receipt time (RFC 3339) from an ack.
func parseReceivedAt(v any) *time.Time {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

func deviceIDFromTopic(topic string) string {
	parts := strings.Split(topic, "/")
	if len(parts) < 2 || parts[0] != "devices" {
		return ""
	}
	return parts[1]
}

func (cl *Client) onMessage(_ paho.Client, msg paho.Message) {
	deviceID := deviceIDFromTopic(msg.Topic())
	if deviceID == "" {
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(msg.Payload(), &payload); err != nil || payload == nil {
		slog.Warn("mqtt_bad_payload", "topic", msg.Topic())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch {
	case strings.HasSuffix(msg.Topic(), "/heartbeat"):
		cl.handleHeartbeat(ctx, deviceID, "heartbeat", payload)
	case strings.HasSuffix(msg.Topic(), "/telemetry"):
		cl.handleHeartbeat(ctx, deviceID, "telemetry", payload)
	case strings.HasSuffix(msg.Topic(), "/cmd/ack"):
		cl.handleAck(ctx, deviceID, payload)
	}
}

func (cl *Client) handleHeartbeat(ctx context.Context, deviceID, kind string, payload map[string]any) {
	if err := cl.svc.RecordHeartbeat(ctx, deviceID); err != nil {
		slog.Error("record_heartbeat_failed", "device_id", deviceID, "error", err)
		return
	}
	cl.m.HeartbeatsReceived.WithLabelValues(deviceID).Inc()
	if err := cl.svc.RecordMessage(ctx, deviceID, kind, payload); err != nil {
		slog.Error("record_message_failed", "device_id", deviceID, "error", err)
	}
}

func (cl *Client) handleAck(ctx context.Context, deviceID string, payload map[string]any) {
	commandID, _ := payload["command_id"].(string)
	if commandID == "" {
		slog.Warn("ack_missing_command_id", "device_id", deviceID)
		return
	}
	status, ok := payload["status"].(string)
	if !ok {
		status = "failed"
	}
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		result = map[string]any{}
	}
	applied, err := cl.svc.ApplyAck(ctx, commandID, status, result, parseReceivedAt(payload["received_at"]))
	if err != nil {
		slog.Error("apply_ack_failed", "command_id", commandID, "error", err)
		return
	}
	if err := cl.svc.RecordMessage(ctx, deviceID, "ack", payload); err != nil {
		slog.Error("record_message_failed", "device_id", deviceID, "error", err)
	}
	if applied {
		cl.m.CommandAcksReceived.WithLabelValues(deviceID, status).Inc()
	}
}

// PublishCommand publishes at QoS 1 and waits briefly for the broker's PUBACK.
// true means the broker accepted it, NOT that the device has received it (it
// may be offline; the broker's persistent session queues it). Device receipt
// is only confirmed by the ack on devices/{id}/cmd/ack.
func (cl *Client) PublishCommand(deviceID, commandID, action string, params map[string]any) bool {
	payload, _ := json.Marshal(map[string]any{"command_id": commandID, "action": action, "params": params})
	t := cl.c.Publish(fmt.Sprintf("devices/%s/cmd", deviceID), 1, false, payload)
	cl.m.CommandsPublished.WithLabelValues(action).Inc()
	return t.WaitTimeout(5*time.Second) && t.Error() == nil
}
