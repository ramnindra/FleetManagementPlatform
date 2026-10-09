package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Settings are the simulator's parameters. Precedence: built-in defaults <
// config file (--config) < command-line flags.
type Settings struct {
	// Cluster is the controller address (host, host:port or URL). It sets both
	// APIEndpoint (http://host:8000 by default) and MQTTHost (host) unless
	// those are set explicitly.
	Cluster           string         `yaml:"cluster"`
	APIEndpoint       string         `yaml:"api_endpoint"`
	MQTTHost          string         `yaml:"mqtt_host"`
	MQTTPort          int            `yaml:"mqtt_port"`
	AdminAPIKey       string         `yaml:"admin_api_key"`
	Count             int            `yaml:"count"`
	DeviceType        string         `yaml:"device_type"`
	DeviceTypes       map[string]int `yaml:"device_types"` // e.g. {switch: 5, gpu: 3}; overrides count/device_type
	HeartbeatInterval time.Duration  `yaml:"heartbeat_interval"`
	Duration          time.Duration  `yaml:"duration"` // 0 = until Ctrl-C
	ProvisionWorkers  int            `yaml:"provision_workers"`
	ConnectWait       time.Duration  `yaml:"connect_wait"`

	// Behavior: make the fleet misbehave on purpose.
	FailureRate   float64       `yaml:"failure_rate"`   // 0..1, fraction of commands that fail
	AckDelay      time.Duration `yaml:"ack_delay"`      // acks delayed by a random 0..ack_delay
	ChurnInterval time.Duration `yaml:"churn_interval"` // mean time between random drops per node; 0 = off
	ChurnDowntime time.Duration `yaml:"churn_downtime"` // how long a dropped node stays offline
}

func defaults() Settings {
	return Settings{
		APIEndpoint: "http://localhost:8000", MQTTHost: "localhost", MQTTPort: 1883, AdminAPIKey: "dev-admin-key",
		Count: 10, DeviceType: "edge", HeartbeatInterval: 10 * time.Second, Duration: 0,
		ProvisionWorkers: 50, ConnectWait: 10 * time.Second, ChurnDowntime: 10 * time.Second,
	}
}

func loadFile(path string, s *Settings) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(raw, s); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// parseSettings applies defaults, then the --config file, then flags.
func parseSettings(args []string) (Settings, error) {
	s := defaults()

	// First pass: find --config so file values can become the flag defaults.
	var cfgPath string
	pre := flag.NewFlagSet("pre", flag.ContinueOnError)
	pre.SetOutput(discard{})
	pre.StringVar(&cfgPath, "config", "", "")
	_ = pre.Parse(args) // unknown flags are handled by the real parse below
	if cfgPath != "" {
		if err := loadFile(cfgPath, &s); err != nil {
			return s, err
		}
	}

	fs := flag.NewFlagSet("simulator", flag.ContinueOnError)
	fs.String("config", cfgPath, "YAML config file (flags override its values)")
	fs.StringVar(&s.Cluster, "cluster", s.Cluster, "controller address: host, host:port or URL; sets --api-endpoint and --mqtt-host")
	fs.StringVar(&s.APIEndpoint, "api-endpoint", s.APIEndpoint, "controller REST endpoint")
	fs.StringVar(&s.MQTTHost, "mqtt-host", s.MQTTHost, "MQTT broker host")
	fs.IntVar(&s.MQTTPort, "mqtt-port", s.MQTTPort, "MQTT broker port")
	fs.StringVar(&s.AdminAPIKey, "admin-api-key", s.AdminAPIKey, "controller admin API key")
	fs.IntVar(&s.Count, "count", s.Count, "number of simulated devices (nodes)")
	fs.StringVar(&s.DeviceType, "device-type", s.DeviceType, "switch | gpu | edge")
	fs.DurationVar(&s.HeartbeatInterval, "heartbeat-interval", s.HeartbeatInterval, "heartbeat interval")
	fs.DurationVar(&s.Duration, "duration", s.Duration, "run time; 0 = until Ctrl-C")
	fs.IntVar(&s.ProvisionWorkers, "provision-workers", s.ProvisionWorkers, "concurrent provisioning workers")
	fs.DurationVar(&s.ConnectWait, "connect-wait", s.ConnectWait, "per-device connect wait before giving up")
	fs.Float64Var(&s.FailureRate, "failure-rate", s.FailureRate, "fraction (0..1) of commands that fail")
	fs.DurationVar(&s.AckDelay, "ack-delay", s.AckDelay, "delay each ack by a random 0..ack-delay")
	fs.DurationVar(&s.ChurnInterval, "churn-interval", s.ChurnInterval, "mean time between random node drops; 0 = off")
	fs.DurationVar(&s.ChurnDowntime, "churn-downtime", s.ChurnDowntime, "how long a dropped node stays offline")
	if err := fs.Parse(args); err != nil {
		return s, err
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if s.Cluster != "" {
		host, api, err := resolveCluster(s.Cluster)
		if err != nil {
			return s, err
		}
		// An explicit api-endpoint / mqtt-host (flag, or a file value that
		// differs from the default) wins over the --cluster shorthand.
		if !set["api-endpoint"] && s.APIEndpoint == defaults().APIEndpoint {
			s.APIEndpoint = api
		}
		if !set["mqtt-host"] && s.MQTTHost == defaults().MQTTHost {
			s.MQTTHost = host
		}
	}
	return s, s.validate()
}

// resolveCluster turns "host", "host:port" or "http(s)://host[:port]" into the
// broker host and the REST endpoint (port 8000 when none is given).
func resolveCluster(c string) (host, api string, err error) {
	if !strings.Contains(c, "://") {
		c = "http://" + c
	}
	u, err := url.Parse(c)
	if err != nil || u.Hostname() == "" {
		return "", "", fmt.Errorf("invalid --cluster %q", c)
	}
	port := u.Port()
	if port == "" {
		port = "8000"
	}
	return u.Hostname(), fmt.Sprintf("%s://%s:%s", u.Scheme, u.Hostname(), port), nil
}

func (s *Settings) validate() error {
	valid := map[string]bool{"switch": true, "gpu": true, "edge": true}
	if len(s.DeviceTypes) == 0 {
		if s.Count < 1 {
			return fmt.Errorf("count must be >= 1")
		}
		if !valid[s.DeviceType] {
			return fmt.Errorf("device-type must be one of switch, gpu, edge")
		}
	}
	for t, n := range s.DeviceTypes {
		if !valid[t] || n < 0 {
			return fmt.Errorf("device_types: invalid entry %s: %d", t, n)
		}
	}
	if s.FailureRate < 0 || s.FailureRate > 1 {
		return fmt.Errorf("failure-rate must be between 0 and 1")
	}
	if s.AckDelay < 0 || s.ChurnInterval < 0 || s.ChurnDowntime < 0 {
		return fmt.Errorf("ack-delay, churn-interval and churn-downtime must not be negative")
	}
	if s.HeartbeatInterval < time.Second {
		return fmt.Errorf("heartbeat-interval must be at least 1s")
	}
	return nil
}

// Types expands the settings into one device type per simulated device.
func (s *Settings) Types() []string {
	if len(s.DeviceTypes) == 0 {
		out := make([]string, s.Count)
		for i := range out {
			out[i] = s.DeviceType
		}
		return out
	}
	names := make([]string, 0, len(s.DeviceTypes))
	for t := range s.DeviceTypes {
		names = append(names, t)
	}
	sort.Strings(names)
	var out []string
	for _, t := range names {
		for i := 0; i < s.DeviceTypes[t]; i++ {
			out = append(out, t)
		}
	}
	return out
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
