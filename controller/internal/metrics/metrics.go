// Package metrics defines the controller's Prometheus metrics.
package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ramnindra/fleetmanagementplatform/controller/internal/store"
)

type Metrics struct {
	Registry            *prometheus.Registry
	HeartbeatsReceived  *prometheus.CounterVec
	CommandsPublished   *prometheus.CounterVec
	CommandAcksReceived *prometheus.CounterVec
	// HTTPDuration is labeled by route TEMPLATE, never the raw path: the raw
	// path would create one time series per device_id (unbounded cardinality).
	HTTPDuration *prometheus.HistogramVec
}

func New(st store.Store, offlineThreshold time.Duration) *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		HeartbeatsReceived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "controller_heartbeats_received_total", Help: "Heartbeats received from devices"}, []string{"device_id"}),
		CommandsPublished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "controller_commands_published_total", Help: "Commands published to devices"}, []string{"action"}),
		CommandAcksReceived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "controller_command_acks_received_total", Help: "Command acknowledgments received"}, []string{"device_id", "status"}),
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "controller_http_request_duration_seconds", Help: "HTTP request latency",
			Buckets: []float64{.005, .01, .025, .05, .075, .1, .25, .5, .75, 1, 2.5, 5, 7.5, 10}},
			[]string{"method", "path", "status_code"}),
	}
	m.Registry.MustRegister(m.HeartbeatsReceived, m.CommandsPublished, m.CommandAcksReceived, m.HTTPDuration,
		&deviceStatusCollector{st: st, threshold: offlineThreshold})
	return m
}

// deviceStatusCollector computes online/offline fresh on every scrape from
// last_seen_at, rather than via counters that can drift (a crashed replica, a
// missed decrement). A pod's in-memory state is never the source of truth.
type deviceStatusCollector struct {
	st        store.Store
	threshold time.Duration
}

var (
	onlineDesc  = prometheus.NewDesc("controller_devices_online", "Devices considered online (heartbeat within threshold)", nil, nil)
	offlineDesc = prometheus.NewDesc("controller_devices_offline", "Registered devices not currently online", nil, nil)
)

func (c *deviceStatusCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- onlineDesc
	ch <- offlineDesc
}

func (c *deviceStatusCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A scrape must never fail over a DB blip or startup race; emit 0/0.
	online, total, err := c.st.CountDevices(ctx, time.Now().UTC().Add(-c.threshold))
	if err != nil {
		online, total = 0, 0
	}
	ch <- prometheus.MustNewConstMetric(onlineDesc, prometheus.GaugeValue, float64(online))
	ch <- prometheus.MustNewConstMetric(offlineDesc, prometheus.GaugeValue, float64(total-online))
}
