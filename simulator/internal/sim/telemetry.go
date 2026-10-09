package sim

import "math/rand/v2"

func uniform(lo, hi float64) float64 { return round1(lo + rand.Float64()*(hi-lo)) }

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

// Telemetry returns plausible synthetic metrics for a device type. Every
// reading is labeled source=simulated, matching the real agent's convention.
func Telemetry(deviceType string) map[string]any {
	switch deviceType {
	case "switch":
		return map[string]any{
			"source": "simulated", "cpu_load_percent": uniform(2, 35), "memory_used_percent": uniform(20, 60),
			"ports_up": 40 + rand.IntN(9), "ports_total": 48,
			"rx_mbps": uniform(100, 9000), "tx_mbps": uniform(100, 9000),
		}
	case "gpu":
		return map[string]any{
			"source": "simulated", "gpu_utilization": uniform(5, 98), "temperature": uniform(35, 84),
			"memory_used_percent": uniform(10, 95), "power_watts": uniform(60, 400),
		}
	default:
		return map[string]any{
			"source": "simulated", "cpu_load_percent": uniform(1, 70), "memory_used_percent": uniform(10, 80),
			"temperature": uniform(30, 70),
		}
	}
}
