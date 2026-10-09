// Package enrollment exchanges a one-time enrollment token for a device credential.
package enrollment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Enroll returns the long-lived credential, or an error on any non-200 response.
func Enroll(apiEndpoint, deviceID, token string) (string, error) {
	url := fmt.Sprintf("%s/api/v1/devices/%s/enroll", strings.TrimRight(apiEndpoint, "/"), deviceID)
	body, _ := json.Marshal(map[string]string{"enrollment_token": token})
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("enrollment request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("enrollment rejected (%d): %s", resp.StatusCode, raw)
	}
	var out struct {
		DeviceCredential string `json:"device_credential"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.DeviceCredential == "" {
		return "", fmt.Errorf("enrollment response missing device_credential")
	}
	slog.Info("enrollment_succeeded", "device_id", deviceID)
	return out.DeviceCredential, nil
}
