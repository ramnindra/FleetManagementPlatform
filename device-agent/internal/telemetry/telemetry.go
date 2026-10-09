// Package telemetry collects device metrics.
//
// For GPU devices with simulate_gpu=false, real values come from a fixed
// nvidia-smi invocation (no shell, no controller-supplied arguments); any
// failure falls back to clearly-labeled simulated values. Callers can check
// the "source" field to tell the two apart.
package telemetry

import (
	"context"
	"math/rand/v2"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

var start = time.Now()

func UptimeSeconds() float64 { return time.Since(start).Seconds() }

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

func uniform(lo, hi float64) float64 { return lo + rand.Float64()*(hi-lo) }

func nvidiaSMI() (map[string]any, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nvidia-smi",
		"--query-gpu=utilization.gpu,temperature.gpu", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, false
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	parts := strings.Split(first, ",")
	if len(parts) != 2 {
		return nil, false
	}
	util, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	temp, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return nil, false
	}
	return map[string]any{"source": "nvml", "gpu_utilization": util, "temperature": temp}, true
}

func gpu(simulate bool) map[string]any {
	if !simulate {
		if m, ok := nvidiaSMI(); ok {
			return m
		}
	}
	return map[string]any{
		"source":          "simulated",
		"gpu_utilization": round1(uniform(5, 95)),
		"temperature":     round1(uniform(35, 80)),
	}
}

func generic() map[string]any {
	return map[string]any{
		"source":              "simulated",
		"cpu_load_percent":    round1(uniform(1, 40)),
		"memory_used_percent": round1(uniform(10, 70)),
	}
}

func Collect(deviceType string, simulateGPU bool) map[string]any {
	m := generic()
	if deviceType == "gpu" {
		m = gpu(simulateGPU)
	}
	m["uptime_seconds"] = round1(UptimeSeconds())
	return m
}
