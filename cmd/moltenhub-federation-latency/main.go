package main

import (
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"moltenhub/internal/cmdutil"
)

type latencySample struct {
	PublishMS  int64
	EndToEndMS int64
	AckMS      int64
}

type latencyStats struct {
	Count int
	Min   int64
	Max   int64
	P50   int64
	P95   int64
	P99   int64
	Avg   float64
}

type directionReport struct {
	Label    string
	Publish  latencyStats
	EndToEnd latencyStats
	Ack      latencyStats
}

func (r directionReport) meetsSLO(sloMS int64) bool {
	return r.EndToEnd.Count > 0 && r.EndToEnd.P95 <= sloMS
}

type runner struct {
	client        *http.Client
	iterations    int
	pullTimeoutMS int
	verbose       bool
}

func main() {
	var cfg struct {
		naBaseURL      string
		euBaseURL      string
		naToken        string
		euToken        string
		naURI          string
		euURI          string
		iterations     int
		pullTimeoutMS  int
		httpTimeoutSec int
		sloMS          int
		verbose        bool
	}

	flag.StringVar(&cfg.naBaseURL, "na-base-url", "https://na.hive.example.com/v1", "NA MoltenHub API base URL")
	flag.StringVar(&cfg.euBaseURL, "eu-base-url", "https://eu.hive.example.com/v1", "EU MoltenHub API base URL")
	flag.StringVar(&cfg.naToken, "na-token", "", "NA agent bearer token")
	flag.StringVar(&cfg.euToken, "eu-token", "", "EU agent bearer token")
	flag.StringVar(&cfg.naURI, "na-uri", "", "NA agent canonical URI")
	flag.StringVar(&cfg.euURI, "eu-uri", "", "EU agent canonical URI")
	flag.IntVar(&cfg.iterations, "iterations", 10, "Number of probe iterations per direction")
	flag.IntVar(&cfg.pullTimeoutMS, "pull-timeout-ms", 5000, "Pull timeout in milliseconds")
	flag.IntVar(&cfg.httpTimeoutSec, "http-timeout-sec", 20, "HTTP client timeout in seconds")
	flag.IntVar(&cfg.sloMS, "slo-ms", 10000, "Federated end-to-end p95 SLO threshold in milliseconds")
	flag.BoolVar(&cfg.verbose, "verbose", false, "Print per-iteration timing")
	flag.Parse()

	if strings.TrimSpace(cfg.naToken) == "" || strings.TrimSpace(cfg.euToken) == "" || strings.TrimSpace(cfg.naURI) == "" || strings.TrimSpace(cfg.euURI) == "" {
		fmt.Fprintln(os.Stderr, "missing required args: -na-token -eu-token -na-uri -eu-uri")
		os.Exit(2)
	}
	if cfg.iterations <= 0 {
		fmt.Fprintln(os.Stderr, "-iterations must be > 0")
		os.Exit(2)
	}
	if cfg.pullTimeoutMS <= 0 {
		fmt.Fprintln(os.Stderr, "-pull-timeout-ms must be > 0")
		os.Exit(2)
	}
	if cfg.httpTimeoutSec <= 0 {
		fmt.Fprintln(os.Stderr, "-http-timeout-sec must be > 0")
		os.Exit(2)
	}
	if cfg.sloMS <= 0 {
		fmt.Fprintln(os.Stderr, "-slo-ms must be > 0")
		os.Exit(2)
	}

	r := runner{
		client: &http.Client{
			Timeout: time.Duration(cfg.httpTimeoutSec) * time.Second,
		},
		iterations:    cfg.iterations,
		pullTimeoutMS: cfg.pullTimeoutMS,
		verbose:       cfg.verbose,
	}

	naToEU, err := r.runDirection(
		"na_to_eu",
		cfg.naBaseURL, cfg.naToken,
		cfg.euBaseURL, cfg.euToken,
		cfg.euURI,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "latency probe failed for na_to_eu: %v\n", err)
		os.Exit(1)
	}

	euToNA, err := r.runDirection(
		"eu_to_na",
		cfg.euBaseURL, cfg.euToken,
		cfg.naBaseURL, cfg.naToken,
		cfg.naURI,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "latency probe failed for eu_to_na: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("SLO target: federated end-to-end p95 < %dms\n", cfg.sloMS)
	printDirectionReport(naToEU)
	printDirectionReport(euToNA)

	if !naToEU.meetsSLO(int64(cfg.sloMS)) || !euToNA.meetsSLO(int64(cfg.sloMS)) {
		fmt.Fprintf(
			os.Stderr,
			"SLO FAILED: na_to_eu_p95=%dms eu_to_na_p95=%dms threshold=%dms\n",
			naToEU.EndToEnd.P95,
			euToNA.EndToEnd.P95,
			cfg.sloMS,
		)
		os.Exit(1)
	}

	fmt.Printf(
		"SLO PASS: na_to_eu_p95=%dms eu_to_na_p95=%dms threshold=%dms\n",
		naToEU.EndToEnd.P95,
		euToNA.EndToEnd.P95,
		cfg.sloMS,
	)
}

func (r runner) runDirection(label, senderBaseURL, senderToken, receiverBaseURL, receiverToken, toAgentURI string) (directionReport, error) {
	samples := make([]latencySample, 0, r.iterations)
	stamp := time.Now().UTC().Format("20060102T150405Z")

	for i := 1; i <= r.iterations; i++ {
		payload := fmt.Sprintf("latency-%s-%s-%02d", label, stamp, i)
		sample, err := r.probeOnce(senderBaseURL, senderToken, receiverBaseURL, receiverToken, toAgentURI, payload)
		if err != nil {
			return directionReport{}, fmt.Errorf("iteration %d: %w", i, err)
		}
		samples = append(samples, sample)
		if r.verbose {
			fmt.Printf(
				"%s iter=%02d publish_ms=%d end_to_end_ms=%d ack_ms=%d\n",
				label,
				i,
				sample.PublishMS,
				sample.EndToEndMS,
				sample.AckMS,
			)
		}
	}

	publishValues := make([]int64, 0, len(samples))
	endToEndValues := make([]int64, 0, len(samples))
	ackValues := make([]int64, 0, len(samples))
	for _, sample := range samples {
		publishValues = append(publishValues, sample.PublishMS)
		endToEndValues = append(endToEndValues, sample.EndToEndMS)
		ackValues = append(ackValues, sample.AckMS)
	}

	return directionReport{
		Label:    label,
		Publish:  computeLatencyStats(publishValues),
		EndToEnd: computeLatencyStats(endToEndValues),
		Ack:      computeLatencyStats(ackValues),
	}, nil
}

func (r runner) probeOnce(senderBaseURL, senderToken, receiverBaseURL, receiverToken, toAgentURI, payload string) (latencySample, error) {
	start := time.Now()

	pubStatus, pubPayload, pubRaw, err := r.requestJSON(senderBaseURL, http.MethodPost, "/messages/publish", map[string]string{
		"Authorization": "Bearer " + senderToken,
	}, map[string]any{
		"to_agent_uri": toAgentURI,
		"content_type": "text/plain",
		"payload":      payload,
	})
	if err != nil {
		return latencySample{}, err
	}
	publishMS := time.Since(start).Milliseconds()
	if pubStatus != http.StatusAccepted {
		return latencySample{}, fmt.Errorf("publish expected 202 got %d body=%s", pubStatus, pubRaw)
	}
	if runtimeStatus(pubPayload) == "dropped" {
		return latencySample{}, fmt.Errorf("publish dropped reason=%q body=%s", cmdutil.AsString(pubPayload, "reason"), pubRaw)
	}

	pullPath := fmt.Sprintf("/messages/pull?timeout_ms=%d", r.pullTimeoutMS)
	pullStatus, pullPayload, pullRaw, err := r.requestJSON(receiverBaseURL, http.MethodGet, pullPath, map[string]string{
		"Authorization": "Bearer " + receiverToken,
	}, nil)
	if err != nil {
		return latencySample{}, err
	}
	endToEndMS := time.Since(start).Milliseconds()
	if pullStatus != http.StatusOK {
		return latencySample{}, fmt.Errorf("pull expected 200 got %d body=%s", pullStatus, pullRaw)
	}
	message, err := cmdutil.RequireObject(pullPayload, "message")
	if err != nil {
		return latencySample{}, err
	}
	if got := cmdutil.AsString(message, "payload"); got != payload {
		return latencySample{}, fmt.Errorf("payload mismatch got=%q want=%q", got, payload)
	}
	delivery, err := cmdutil.RequireObject(pullPayload, "delivery")
	if err != nil {
		return latencySample{}, err
	}
	deliveryID := cmdutil.AsString(delivery, "delivery_id")
	if deliveryID == "" {
		return latencySample{}, fmt.Errorf("delivery_id missing in pull payload=%v", pullPayload)
	}

	ackStart := time.Now()
	ackStatus, _, ackRaw, err := r.requestJSON(receiverBaseURL, http.MethodPost, "/messages/ack", map[string]string{
		"Authorization": "Bearer " + receiverToken,
	}, map[string]any{
		"delivery_id": deliveryID,
	})
	if err != nil {
		return latencySample{}, err
	}
	ackMS := time.Since(ackStart).Milliseconds()
	if ackStatus != http.StatusOK {
		return latencySample{}, fmt.Errorf("ack expected 200 got %d body=%s", ackStatus, ackRaw)
	}

	return latencySample{
		PublishMS:  publishMS,
		EndToEndMS: endToEndMS,
		AckMS:      ackMS,
	}, nil
}

func (r runner) requestJSON(baseURL, method, path string, headers map[string]string, body any) (int, map[string]any, string, error) {
	resp, err := cmdutil.RequestJSON(r.client, baseURL, method, path, headers, body)
	if err != nil {
		return 0, nil, "", err
	}
	return resp.StatusCode, resp.Payload, resp.Raw, nil
}

func runtimeStatus(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if top := cmdutil.AsString(payload, "status"); top != "" {
		return top
	}
	result, ok := payload["result"].(map[string]any)
	if !ok {
		return ""
	}
	return cmdutil.AsString(result, "status")
}

func computeLatencyStats(values []int64) latencyStats {
	if len(values) == 0 {
		return latencyStats{}
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum int64
	for _, value := range sorted {
		sum += value
	}

	return latencyStats{
		Count: len(sorted),
		Min:   sorted[0],
		Max:   sorted[len(sorted)-1],
		P50:   percentileNearestRank(sorted, 0.50),
		P95:   percentileNearestRank(sorted, 0.95),
		P99:   percentileNearestRank(sorted, 0.99),
		Avg:   float64(sum) / float64(len(sorted)),
	}
}

func percentileNearestRank(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	rank := int(math.Ceil(float64(len(sorted))*p)) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

func printDirectionReport(report directionReport) {
	fmt.Printf(
		"%s samples=%d publish_ms(avg=%.1f p50=%d p95=%d p99=%d max=%d) e2e_ms(avg=%.1f p50=%d p95=%d p99=%d max=%d) ack_ms(avg=%.1f p50=%d p95=%d p99=%d max=%d)\n",
		report.Label,
		report.EndToEnd.Count,
		report.Publish.Avg, report.Publish.P50, report.Publish.P95, report.Publish.P99, report.Publish.Max,
		report.EndToEnd.Avg, report.EndToEnd.P50, report.EndToEnd.P95, report.EndToEnd.P99, report.EndToEnd.Max,
		report.Ack.Avg, report.Ack.P50, report.Ack.P95, report.Ack.P99, report.Ack.Max,
	)
}
