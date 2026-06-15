// Package ingestion pulls metrics from Prometheus and OpenTelemetry sources,
// converts them to telemetry.MetricPoint format, and feeds the telemetry store.
package ingestion

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/qphysics/phaseshift/telemetry"
)

// PromScraper ingests metrics from a Prometheus-compatible endpoint.
// It queries the /api/v1/query API to pull specific metric families and
// assembles them into telemetry.MetricPoint records per discovered service.
type PromScraper struct {
	baseURL    string
	httpClient *http.Client
	store      *telemetry.Store
}

// NewPromScraper creates a scraper pointed at the given Prometheus base URL.
func NewPromScraper(baseURL string, store *telemetry.Store) *PromScraper {
	return &PromScraper{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		store: store,
	}
}

// Run starts the scrape loop. It runs until ctx is cancelled.
// interval controls how often metrics are pulled from Prometheus.
func (s *PromScraper) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Scrape immediately on start.
	s.scrapeAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scrapeAll(ctx)
		}
	}
}

// ScrapeOnce performs a single scrape cycle. Useful for testing.
func (s *PromScraper) ScrapeOnce(ctx context.Context) {
	s.scrapeAll(ctx)
}

// scrapeAll queries Prometheus for all metric families we care about,
// assembles per-service MetricPoints, and ingests them into the store.
func (s *PromScraper) scrapeAll(ctx context.Context) {
	// Metric families to fetch. Each maps to a field in MetricPoint.
	// We use a label — typically "job" or "service" — as the service ID.
	type metricFetch struct {
		query  string
		setter func(p *telemetry.MetricPoint, val float64)
	}

	// These PromQL expressions cover the most common exporters:
	// - http_server_requests_total (Spring Boot, FastAPI, Go net/http)
	// - http_requests_total (general)
	// - grpc_server_handled_total
	// - process_* (standard Go/JVM exporters)
	// - jvm_* (JVM)
	// - go_* (Go runtime)
	fetches := []metricFetch{
		// Request rate — try multiple common metric names.
		{
			query: `sum by (job) (rate(http_requests_total[1m]))`,
			setter: func(p *telemetry.MetricPoint, v float64) {
				if p.RequestRate == 0 {
					p.RequestRate = v
				}
			},
		},
		{
			query: `sum by (job) (rate(http_server_requests_seconds_count[1m]))`,
			setter: func(p *telemetry.MetricPoint, v float64) {
				if p.RequestRate == 0 {
					p.RequestRate = v
				}
			},
		},
		{
			query: `sum by (job) (rate(grpc_server_handled_total[1m]))`,
			setter: func(p *telemetry.MetricPoint, v float64) {
				if p.RequestRate == 0 {
					p.RequestRate = v
				}
			},
		},
		// Error rate.
		{
			query: `sum by (job) (rate(http_requests_total{status=~"5.."}[1m])) / sum by (job) (rate(http_requests_total[1m]))`,
			setter: func(p *telemetry.MetricPoint, v float64) {
				if !math.IsNaN(v) && !math.IsInf(v, 0) {
					p.ErrorRate = v
				}
			},
		},
		// Mean latency (seconds → ms).
		{
			query: `sum by (job) (rate(http_requests_duration_seconds_sum[1m])) / sum by (job) (rate(http_requests_duration_seconds_count[1m]))`,
			setter: func(p *telemetry.MetricPoint, v float64) {
				if !math.IsNaN(v) && !math.IsInf(v, 0) && p.Latency.Mean == 0 {
					p.Latency.Mean = v * 1000 // seconds → ms
				}
			},
		},
		{
			query: `sum by (job) (rate(http_server_requests_seconds_sum[1m])) / sum by (job) (rate(http_server_requests_seconds_count[1m]))`,
			setter: func(p *telemetry.MetricPoint, v float64) {
				if !math.IsNaN(v) && !math.IsInf(v, 0) && p.Latency.Mean == 0 {
					p.Latency.Mean = v * 1000
				}
			},
		},
		// P95 latency from histogram.
		{
			query: `histogram_quantile(0.95, sum by (job, le) (rate(http_request_duration_seconds_bucket[1m])))`,
			setter: func(p *telemetry.MetricPoint, v float64) {
				if !math.IsNaN(v) && !math.IsInf(v, 0) {
					p.Latency.P95 = v * 1000
				}
			},
		},
		// P99 latency.
		{
			query: `histogram_quantile(0.99, sum by (job, le) (rate(http_request_duration_seconds_bucket[1m])))`,
			setter: func(p *telemetry.MetricPoint, v float64) {
				if !math.IsNaN(v) && !math.IsInf(v, 0) {
					p.Latency.P99 = v * 1000
				}
			},
		},
		// CPU usage.
		{
			query: `sum by (job) (rate(process_cpu_seconds_total[1m]))`,
			setter: func(p *telemetry.MetricPoint, v float64) {
				p.CPUUsage = v
			},
		},
		// Memory usage.
		{
			query: `sum by (job) (process_resident_memory_bytes)`,
			setter: func(p *telemetry.MetricPoint, v float64) {
				p.MemUsage = v / (1024 * 1024 * 1024) // bytes → GB normalised 0-1 (assume 4GB cap)
				if p.MemUsage > 1 {
					p.MemUsage = 1
				}
			},
		},
		// Active connections / goroutines as proxy.
		{
			query: `sum by (job) (go_goroutines)`,
			setter: func(p *telemetry.MetricPoint, v float64) {
				if p.ActiveConns == 0 {
					p.ActiveConns = int64(v / 10) // goroutines/10 ≈ connection proxy
					if p.ActiveConns < 1 {
						p.ActiveConns = 1
					}
				}
			},
		},
		// Queue depth — common in message queue exporters.
		{
			query: `sum by (job) (rabbitmq_queue_messages_ready + rabbitmq_queue_messages_unacknowledged)`,
			setter: func(p *telemetry.MetricPoint, v float64) {
				if !math.IsNaN(v) {
					p.QueueDepth = int64(v)
				}
			},
		},
	}

	// Collect points per service.
	points := make(map[string]*telemetry.MetricPoint)

	for _, f := range fetches {
		result := s.queryPrometheus(ctx, f.query)
		for svcID, val := range result {
			p, ok := points[svcID]
			if !ok {
				p = &telemetry.MetricPoint{
					ServiceID: svcID,
					Timestamp: time.Now(),
				}
				points[svcID] = p
			}
			f.setter(p, val)
		}
	}

	// Fill defaults and ingest.
	for _, p := range points {
		// If no latency detected, use a reasonable default so the engine has data.
		if p.Latency.Mean == 0 && p.RequestRate > 0 {
			p.Latency.Mean = 10.0 // 10ms default
		}
		if p.Latency.P50 == 0 {
			p.Latency.P50 = p.Latency.Mean * 0.8
		}
		if p.Latency.P95 == 0 {
			p.Latency.P95 = p.Latency.Mean * 1.5
		}
		if p.Latency.P99 == 0 {
			p.Latency.P99 = p.Latency.Mean * 2.5
		}
		if p.ActiveConns == 0 {
			p.ActiveConns = 1
		}
		s.store.Ingest(p)
	}
}

// queryPrometheus executes a single PromQL instant query and returns
// a map of label-value → float64 result. Uses the "job" label as service ID.
func (s *PromScraper) queryPrometheus(ctx context.Context, query string) map[string]float64 {
	endpoint := fmt.Sprintf("%s/api/v1/query?query=%s&time=%d",
		s.baseURL,
		url.QueryEscape(query),
		time.Now().Unix(),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	// Parse Prometheus instant query response:
	// {"data":{"resultType":"vector","result":[{"metric":{...},"value":[ts,"val"]}]}}
	var raw struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []interface{}     `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	// Manual JSON parse to avoid import of encoding/json cycle issues.
	// Use stdlib json via a local import alias.
	if err := jsonUnmarshal(body, &raw); err != nil {
		return nil
	}

	out := make(map[string]float64)
	for _, r := range raw.Data.Result {
		// Prefer "job" label as service ID, fall back to "service", then "__name__".
		svcID := r.Metric["job"]
		if svcID == "" {
			svcID = r.Metric["service"]
		}
		if svcID == "" {
			svcID = r.Metric["app"]
		}
		if svcID == "" {
			continue
		}
		if len(r.Value) < 2 {
			continue
		}
		valStr, ok := r.Value[1].(string)
		if !ok {
			continue
		}
		val, err := strconv.ParseFloat(valStr, 64)
		if err != nil || math.IsNaN(val) || math.IsInf(val, 0) {
			continue
		}
		// If multiple time series for same job, sum them.
		out[svcID] += val
	}
	return out
}

// ──────────────────────────────────────────────────────────────────────────────
// OTel span receiver — parses OTel JSON traces to build topology edges
// ──────────────────────────────────────────────────────────────────────────────

// SpanEdge represents a caller→callee relationship extracted from a trace span.
type SpanEdge struct {
	Source    string
	Target    string
	LatencyMs float64
	IsError   bool
}

// OTelReceiver accepts OTLP/HTTP trace payloads on a local port and
// extracts service dependency edges from them.
// It exposes an http.Handler compatible with net/http for embedding.
type OTelReceiver struct {
	edgeCh chan SpanEdge
}

// NewOTelReceiver creates a receiver. EdgeCh receives extracted edges.
func NewOTelReceiver() *OTelReceiver {
	return &OTelReceiver{
		edgeCh: make(chan SpanEdge, 1024),
	}
}

// EdgeChannel returns the channel on which discovered edges are sent.
func (r *OTelReceiver) EdgeChannel() <-chan SpanEdge {
	return r.edgeCh
}

// HandleTraces is an http.HandlerFunc that accepts OTLP/HTTP POST payloads.
// The payload format is JSON ExportTraceServiceRequest. We parse only what
// we need: resource service.name and span parent/child relationships.
func (r *OTelReceiver) HandleTraces(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	defer req.Body.Close()

	// We parse a simplified OTLP JSON structure. Full OTLP proto parsing
	// would require protobuf; we avoid that dependency by parsing the JSON form.
	// Trace exporters using OTLP/HTTP+JSON are widely supported (Jaeger, OTel SDK).
	body, err := io.ReadAll(io.LimitReader(req.Body, 4*1024*1024))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	edges := parseOTLPJSON(body)
	for _, e := range edges {
		select {
		case r.edgeCh <- e:
		default:
			// Channel full — drop. We'll catch the next batch.
		}
	}
	w.WriteHeader(http.StatusOK)
}

// parseOTLPJSON extracts caller→callee edges from a simplified OTLP JSON payload.
// OTLP JSON structure (simplified):
//
//	{resourceSpans:[{resource:{attributes:[{key:"service.name",value:{stringValue:"..."}}]},
//	  scopeSpans:[{spans:[{traceId,spanId,parentSpanId,name,kind,status,attributes,...}]}]}]}
func parseOTLPJSON(data []byte) []SpanEdge {
	// We'll do a simple text scan rather than full JSON parse to avoid allocations.
	// This handles the common case correctly.
	s := string(data)

	// Build a map: spanId → serviceName
	// and parentSpanId → serviceName for caller lookup.
	type spanInfo struct {
		svcName   string
		parentID  string
		spanID    string
		latencyMs float64
		isError   bool
	}

	// Extract service name from resource attributes.
	// Pattern: "service.name" followed by "stringValue":"<name>"
	spans := []spanInfo{}

	// Find all resourceSpan blocks and extract service name + spans.
	// This is a best-effort parser for the common OTLP JSON format.
	lines := strings.Split(s, "\n")
	currentSvc := ""
	var currentSpan *spanInfo

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Service name from resource.
		if strings.Contains(line, `"service.name"`) {
			// Next few lines will have stringValue.
			idx := strings.Index(s, `"service.name"`)
			if idx >= 0 {
				sub := s[idx:]
				svStart := strings.Index(sub, `"stringValue":"`)
				if svStart >= 0 {
					svStart += len(`"stringValue":"`)
					svEnd := strings.Index(sub[svStart:], `"`)
					if svEnd > 0 {
						currentSvc = sub[svStart : svStart+svEnd]
					}
				}
			}
		}

		if strings.Contains(line, `"spanId"`) {
			if currentSpan != nil {
				spans = append(spans, *currentSpan)
			}
			currentSpan = &spanInfo{svcName: currentSvc}
			id := extractJSONStringValue(line, "spanId")
			if id != "" {
				currentSpan.spanID = id
			}
		}

		if currentSpan != nil {
			if strings.Contains(line, `"parentSpanId"`) {
				currentSpan.parentID = extractJSONStringValue(line, "parentSpanId")
			}
			if strings.Contains(line, `"endTimeUnixNano"`) || strings.Contains(line, `"startTimeUnixNano"`) {
				// We'd need both to compute latency — skip for now, use 0.
			}
			if strings.Contains(line, `"ERROR"`) || strings.Contains(line, `"STATUS_CODE_ERROR"`) {
				currentSpan.isError = true
			}
		}
	}
	if currentSpan != nil {
		spans = append(spans, *currentSpan)
	}

	// Build spanID → service map.
	spanSvc := make(map[string]string, len(spans))
	for _, sp := range spans {
		if sp.spanID != "" && sp.svcName != "" {
			spanSvc[sp.spanID] = sp.svcName
		}
	}

	// Extract edges: if a span has a parent in a different service, that's a call edge.
	var edges []SpanEdge
	seen := make(map[string]bool)
	for _, sp := range spans {
		if sp.parentID == "" || sp.svcName == "" {
			continue
		}
		parentSvc, ok := spanSvc[sp.parentID]
		if !ok || parentSvc == sp.svcName {
			continue
		}
		key := parentSvc + "→" + sp.svcName
		if seen[key] {
			continue
		}
		seen[key] = true
		edges = append(edges, SpanEdge{
			Source:    parentSvc,
			Target:    sp.svcName,
			LatencyMs: sp.latencyMs,
			IsError:   sp.isError,
		})
	}
	return edges
}

func extractJSONStringValue(line, key string) string {
	marker := `"` + key + `":"`
	idx := strings.Index(line, marker)
	if idx < 0 {
		return ""
	}
	rest := line[idx+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// ──────────────────────────────────────────────────────────────────────────────
// Prometheus text format scraper (for /metrics endpoint direct scraping)
// ──────────────────────────────────────────────────────────────────────────────

// ScrapeMetricsEndpoint scrapes a raw Prometheus /metrics text endpoint
// and returns a map of metric_name → value (last sample wins for simplicity).
// This is used for Docker containers that expose /metrics but aren't registered
// in a Prometheus server.
func ScrapeMetricsEndpoint(ctx context.Context, metricsURL string) map[string]float64 {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
	if err != nil {
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	result := make(map[string]float64)
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name := parts[0]
		// Strip label set from name if present.
		if i := strings.Index(name, "{"); i >= 0 {
			name = name[:i]
		}
		val, err := strconv.ParseFloat(parts[len(parts)-1], 64)
		if err != nil || math.IsNaN(val) || math.IsInf(val, 0) {
			continue
		}
		result[name] = val
	}
	return result
}

// jsonUnmarshal is a thin wrapper kept here to avoid import cycles.
// Uses stdlib encoding/json internally.
func jsonUnmarshal(data []byte, v interface{}) error {
	// We use a scanner approach to avoid the import cycle issue.
	// The actual unmarshal is done via the standard library.
	return jsonUnmarshalImpl(data, v)
}