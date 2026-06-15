// Package discovery auto-detects telemetry sources with zero user configuration.
// It probes well-known endpoints and environment variables to determine what
// is available in the runtime environment.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// SourceType identifies the kind of telemetry source discovered.
type SourceType string

const (
	SourcePrometheus   SourceType = "prometheus"
	SourceOTelCollector SourceType = "otel-collector"
	SourceDocker       SourceType = "docker"
	SourceKubernetes   SourceType = "kubernetes"
)

// Source represents one discovered telemetry endpoint.
type Source struct {
	Type        SourceType
	URL         string
	Healthy     bool
	Description string
	Services    []string // service names found (if discoverable at detection time)
}

// Environment summarises what was found during auto-discovery.
type Environment struct {
	Sources        []Source
	IsKubernetes   bool
	IsDocker       bool
	PrometheusURLs []string
	OTelGRPCPort   int  // 4317
	OTelHTTPPort   int  // 4318
	DiscoveredAt   time.Time
}

// Summary returns a human-readable status string for the dashboard.
func (e *Environment) Summary() string {
	var parts []string
	for _, s := range e.Sources {
		status := "✓"
		if !s.Healthy {
			status = "✗"
		}
		parts = append(parts, fmt.Sprintf("%s %s (%s)", status, s.Type, s.URL))
	}
	if len(parts) == 0 {
		return "No telemetry sources detected. Run with --demo for simulated data."
	}
	return strings.Join(parts, " | ")
}

// HasAnySource returns true if at least one healthy source was found.
func (e *Environment) HasAnySource() bool {
	for _, s := range e.Sources {
		if s.Healthy {
			return true
		}
	}
	return false
}

var probeClient = &http.Client{
	Timeout: 2 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{Timeout: 1 * time.Second}).DialContext,
	},
}

// Discover probes the local environment and returns everything it finds.
// It never blocks longer than ~10 seconds total.
func Discover(ctx context.Context) *Environment {
	env := &Environment{
		DiscoveredAt: time.Now(),
	}

	// Detect Kubernetes first (fast env var check).
	env.IsKubernetes = detectKubernetes()

	// Detect Docker socket.
	env.IsDocker = detectDocker()

	// Probe Prometheus on all well-known ports.
	promURLs := candidatePrometheusURLs(env)
	for _, u := range promURLs {
		src := probePrometheus(ctx, u)
		if src.Healthy {
			env.PrometheusURLs = append(env.PrometheusURLs, u)
		}
		env.Sources = append(env.Sources, src)
	}

	// Probe OpenTelemetry Collector.
	otelSrc := probeOTelCollector(ctx)
	env.Sources = append(env.Sources, otelSrc)
	if otelSrc.Healthy {
		env.OTelGRPCPort = 4317
		env.OTelHTTPPort = 4318
	}

	// If Docker is available, list running containers as additional service hints.
	if env.IsDocker {
		dockerSrc := probeDockerAPI(ctx)
		env.Sources = append(env.Sources, dockerSrc)
	}

	return env
}

func detectKubernetes() bool {
	// Standard K8s in-cluster signal: service account token exists.
	if _, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token"); err == nil {
		return true
	}
	// KUBECONFIG env var set.
	if os.Getenv("KUBECONFIG") != "" {
		return true
	}
	// K8s service environment variables injected by the API server.
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

func detectDocker() bool {
	_, err := os.Stat("/var/run/docker.sock")
	return err == nil
}

// candidatePrometheusURLs returns the list of Prometheus endpoints to try.
// Order: env var override → kubernetes service DNS → localhost variants.
func candidatePrometheusURLs(env *Environment) []string {
	candidates := []string{}

	// Explicit override wins.
	if u := os.Getenv("QPHYSICS_PROMETHEUS_URL"); u != "" {
		candidates = append(candidates, u)
		return candidates
	}

	// Kubernetes: Prometheus is commonly at a cluster service.
	if env.IsKubernetes {
		ns := os.Getenv("POD_NAMESPACE")
		if ns == "" {
			ns = "monitoring"
		}
		candidates = append(candidates,
			fmt.Sprintf("http://prometheus.%s.svc.cluster.local:9090", ns),
			"http://prometheus-server:9090",
			"http://prometheus-operated:9090",
			"http://kube-prometheus-stack-prometheus:9090",
		)
	}

	// Localhost variants (Docker Compose + bare-metal).
	candidates = append(candidates,
		"http://localhost:9090",
		"http://localhost:9091",
		"http://prometheus:9090",
	)

	return candidates
}

func probePrometheus(ctx context.Context, baseURL string) Source {
	src := Source{
		Type:        SourcePrometheus,
		URL:         baseURL,
		Description: "Prometheus metrics endpoint",
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/-/healthy", nil)
	if err != nil {
		return src
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return src
	}
	defer resp.Body.Close()
	src.Healthy = resp.StatusCode == http.StatusOK
	if src.Healthy {
		// Also try to discover service names from labels.
		src.Services = discoverServicesFromPrometheus(ctx, baseURL)
		src.Description = fmt.Sprintf("Prometheus (found %d services)", len(src.Services))
	}
	return src
}

// discoverServicesFromPrometheus queries the Prometheus targets API to
// enumerate running services. Returns job names as service identifiers.
func discoverServicesFromPrometheus(ctx context.Context, baseURL string) []string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/api/v1/targets?state=active", nil)
	if err != nil {
		return nil
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	// Parse minimal structure: {"data":{"activeTargets":[{"labels":{"job":"..."},...}]}}
	var result struct {
		Data struct {
			ActiveTargets []struct {
				Labels map[string]string `json:"labels"`
			} `json:"activeTargets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var services []string
	for _, t := range result.Data.ActiveTargets {
		job := t.Labels["job"]
		if job == "" {
			job = t.Labels["service"]
		}
		if job != "" && !seen[job] {
			seen[job] = true
			services = append(services, job)
		}
	}
	return services
}

func probeOTelCollector(ctx context.Context) Source {
	src := Source{
		Type:        SourceOTelCollector,
		URL:         "localhost:4318",
		Description: "OpenTelemetry Collector (HTTP)",
	}

	// Check OTel HTTP endpoint — it returns 200 on GET /
	otelURL := "http://localhost:4318"
	if u := os.Getenv("QPHYSICS_OTEL_URL"); u != "" {
		otelURL = u
	}
	src.URL = otelURL

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, otelURL, nil)
	if err != nil {
		return src
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		// Try gRPC port reachability via TCP.
		conn, tcpErr := net.DialTimeout("tcp", "localhost:4317", time.Second)
		if tcpErr == nil {
			conn.Close()
			src.Healthy = true
			src.URL = "localhost:4317 (gRPC)"
			src.Description = "OpenTelemetry Collector (gRPC)"
		}
		return src
	}
	defer resp.Body.Close()
	// OTel collector returns 405 on GET (not 200) but it means it's alive.
	src.Healthy = resp.StatusCode < 500
	return src
}

// probeDockerAPI queries the local Docker socket for running containers.
func probeDockerAPI(ctx context.Context) Source {
	src := Source{
		Type:        SourceDocker,
		URL:         "unix:///var/run/docker.sock",
		Description: "Docker Engine API",
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", "/var/run/docker.sock")
		},
	}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://docker/containers/json?filters=%7B%22status%22%3A%5B%22running%22%5D%7D", nil)
	if err != nil {
		return src
	}
	resp, err := client.Do(req)
	if err != nil {
		return src
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return src
	}

	var containers []struct {
		Names  []string          `json:"Names"`
		Labels map[string]string `json:"Labels"`
		State  string            `json:"State"`
	}
	if err := json.Unmarshal(body, &containers); err != nil {
		return src
	}

	src.Healthy = true
	for _, c := range containers {
		name := ""
		if svc, ok := c.Labels["com.docker.compose.service"]; ok {
			name = svc
		} else if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		if name != "" {
			src.Services = append(src.Services, name)
		}
	}
	src.Description = fmt.Sprintf("Docker (%d running containers)", len(src.Services))
	return src
}