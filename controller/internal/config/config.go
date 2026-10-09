// Package config loads controller settings from CONTROLLER_* environment variables.
package config

import (
	"os"
	"strconv"
	"strings"
)

type Settings struct {
	HTTPAddr    string
	DatabaseURL string

	// Sized for provisioning bursts, not steady state: heartbeats/acks use their
	// own short-lived queries, but bulk-enrolling hundreds of devices
	// concurrently (see provisioning/scripts, simulator/load_test) means many
	// register/enroll HTTP requests holding a connection at once. Keep
	// replicas * DBMaxConns under the database's max_connections.
	DBMaxConns int

	MQTTHost            string
	MQTTPort            int
	MQTTClientUsername  string
	MQTTClientPassword  string
	MQTTKeepaliveSecond int

	// Shared secret EMQX must present when calling /internal/mqtt/*. Dev-only
	// shortcut: production should use mTLS between EMQX and the controller
	// (see docs/architecture.md).
	MQTTWebhookSharedSecret string

	AdminAPIKey string

	EnrollmentTokenTTLSeconds int
	OfflineThresholdSeconds   int

	LogLevel    string
	Environment string
}

func Load() Settings {
	return Settings{
		HTTPAddr:                  str("HTTP_ADDR", ":8000"),
		DatabaseURL:               normalizeDatabaseURL(str("DATABASE_URL", "postgres://controller:controller@localhost:5432/controller")),
		DBMaxConns:                num("DB_MAX_CONNS", 80),
		MQTTHost:                  str("MQTT_HOST", "localhost"),
		MQTTPort:                  num("MQTT_PORT", 1883),
		MQTTClientUsername:        str("MQTT_CLIENT_USERNAME", "controller-service"),
		MQTTClientPassword:        str("MQTT_CLIENT_PASSWORD", "change-me-controller-secret"),
		MQTTKeepaliveSecond:       num("MQTT_KEEPALIVE_SECONDS", 30),
		MQTTWebhookSharedSecret:   str("MQTT_WEBHOOK_SHARED_SECRET", "change-me-webhook-secret"),
		AdminAPIKey:               str("ADMIN_API_KEY", "change-me-admin-key"),
		EnrollmentTokenTTLSeconds: num("ENROLLMENT_TOKEN_TTL_SECONDS", 3600),
		OfflineThresholdSeconds:   num("OFFLINE_THRESHOLD_SECONDS", 90),
		LogLevel:                  str("LOG_LEVEL", "INFO"),
		Environment:               str("ENVIRONMENT", "development"),
	}
}

// normalizeDatabaseURL accepts the SQLAlchemy-style "postgresql+psycopg://"
// scheme that existing Helm values / Secrets Manager entries use.
func normalizeDatabaseURL(u string) string {
	if i := strings.Index(u, "+"); i > 0 && i < strings.Index(u, "://") {
		return u[:i] + u[strings.Index(u, "://"):]
	}
	return u
}

func str(key, def string) string {
	if v, ok := os.LookupEnv("CONTROLLER_" + key); ok && v != "" {
		return v
	}
	return def
}

func num(key string, def int) int {
	if v, ok := os.LookupEnv("CONTROLLER_" + key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
