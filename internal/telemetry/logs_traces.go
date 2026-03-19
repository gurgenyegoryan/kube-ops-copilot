package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
)

type BackendProbe struct {
	Backend        Backend
	Reachable      bool
	Signals        []string
	Unknowns       []string
	TopErrorPods   []LogSample
	TracedServices []string
	HealthStatus   string
}

type LogSample struct {
	Namespace string
	Pod       string
	Value     float64
}

func LogsBackends(backends []Backend) []Backend {
	out := []Backend{}
	for _, backend := range backends {
		if backend.Kind == KindLogs {
			out = append(out, backend)
		}
	}
	return out
}

func TracesBackends(backends []Backend) []Backend {
	out := []Backend{}
	for _, backend := range backends {
		if backend.Kind == KindTraces {
			out = append(out, backend)
		}
	}
	return out
}

func ProbeLogsBackend(ctx context.Context, client *kube.Client, backend Backend) BackendProbe {
	probe := BackendProbe{Backend: backend}
	switch backend.Vendor {
	case "loki":
		labels, err := queryLokiLabels(ctx, client, backend)
		if err != nil {
			probe.Unknowns = append(probe.Unknowns, "Loki backend detected but label discovery failed: "+strings.TrimSpace(err.Error()))
			return probe
		}
		probe.Reachable = true
		probe.Signals = append(probe.Signals, fmt.Sprintf("reachable labels=%s", strings.Join(labels, ",")))
		if samples, expr, err := queryLokiErrors(ctx, client, backend, labels); err == nil && len(samples) > 0 {
			probe.TopErrorPods = samples
			probe.Signals = append(probe.Signals, "log hotspot expr="+expr)
		} else if err != nil {
			probe.Unknowns = append(probe.Unknowns, "Loki backend is reachable, but standardized namespace/pod error query was not usable in this run.")
		}
	case "elasticsearch", "opensearch":
		status, err := probeSearchHealth(ctx, client, backend)
		if err != nil {
			probe.Unknowns = append(probe.Unknowns, backend.Vendor+" backend detected but health probe failed: "+strings.TrimSpace(err.Error()))
			return probe
		}
		probe.Reachable = true
		probe.HealthStatus = status
		probe.Signals = append(probe.Signals, "reachable cluster health endpoint status="+status)
	default:
		probe.Unknowns = append(probe.Unknowns, "Unsupported logs backend vendor: "+backend.Vendor)
	}
	return probe
}

func ProbeTracesBackend(ctx context.Context, client *kube.Client, backend Backend) BackendProbe {
	probe := BackendProbe{Backend: backend}
	switch backend.Vendor {
	case "jaeger":
		services, err := queryJaegerServices(ctx, client, backend)
		if err != nil {
			probe.Unknowns = append(probe.Unknowns, "Jaeger backend detected but services probe failed: "+strings.TrimSpace(err.Error()))
			return probe
		}
		probe.Reachable = true
		probe.TracedServices = services
		probe.Signals = append(probe.Signals, fmt.Sprintf("reachable tracedServices=%d", len(services)))
		if len(services) > 0 {
			ops, err := queryJaegerOperations(ctx, client, backend, services[0])
			if err == nil && len(ops) > 0 {
				probe.Signals = append(probe.Signals, fmt.Sprintf("sample service %s operations=%d", services[0], len(ops)))
			}
		}
	case "tempo":
		signals, err := queryTempoProbe(ctx, client, backend)
		if err != nil {
			probe.Unknowns = append(probe.Unknowns, "Tempo backend detected but probe failed: "+strings.TrimSpace(err.Error()))
			return probe
		}
		probe.Reachable = true
		probe.Signals = append(probe.Signals, signals...)
	default:
		probe.Unknowns = append(probe.Unknowns, "Unsupported traces backend vendor: "+backend.Vendor)
	}
	return probe
}

func queryLokiLabels(ctx context.Context, client *kube.Client, backend Backend) ([]string, error) {
	body, err := proxyGetRaw(ctx, client, backend, joinProxyPath(backend.PathPrefixes[0], "labels"), nil)
	if err != nil {
		return nil, err
	}
	resp := struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
	}{}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Status != "success" {
		return nil, fmt.Errorf("loki labels response status=%s", resp.Status)
	}
	sort.Strings(resp.Data)
	return resp.Data, nil
}

func queryLokiErrors(ctx context.Context, client *kube.Client, backend Backend, labels []string) ([]LogSample, string, error) {
	namespaceLabel, podLabel := detectLokiLabelPair(labels)
	if namespaceLabel == "" || podLabel == "" {
		return nil, "", fmt.Errorf("no standard namespace/pod labels found")
	}
	expressions := []string{
		fmt.Sprintf(`topk(5, sum by (%s,%s) (count_over_time({%s=~".+",%s=~".+"} |~ "(?i)(error|exception|panic|fatal)" [15m])))`, namespaceLabel, podLabel, namespaceLabel, podLabel),
		fmt.Sprintf(`topk(5, sum by (%s,%s) (count_over_time({%s=~".+",%s=~".+"} |= "error" [15m])))`, namespaceLabel, podLabel, namespaceLabel, podLabel),
	}
	var lastErr error
	for _, expr := range expressions {
		body, err := proxyGetRaw(ctx, client, backend, joinProxyPath(backend.PathPrefixes[0], "query"), map[string]string{"query": expr, "time": fmt.Sprintf("%d", time.Now().Unix())})
		if err != nil {
			lastErr = err
			continue
		}
		resp := struct {
			Status string `json:"status"`
			Data   struct {
				ResultType string `json:"resultType"`
				Result     []struct {
					Metric map[string]string `json:"metric"`
					Value  []any             `json:"value"`
				} `json:"result"`
			} `json:"data"`
		}{}
		if err := json.Unmarshal(body, &resp); err != nil {
			lastErr = err
			continue
		}
		if resp.Status != "success" || resp.Data.ResultType != "vector" {
			lastErr = fmt.Errorf("loki query returned status=%s resultType=%s", resp.Status, resp.Data.ResultType)
			continue
		}
		samples := make([]LogSample, 0, len(resp.Data.Result))
		for _, item := range resp.Data.Result {
			value, ok := parseLokiValue(item.Value)
			if !ok {
				continue
			}
			samples = append(samples, LogSample{
				Namespace: item.Metric[namespaceLabel],
				Pod:       item.Metric[podLabel],
				Value:     value,
			})
		}
		return samples, expr, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no Loki query expression succeeded")
	}
	return nil, "", lastErr
}

func probeSearchHealth(ctx context.Context, client *kube.Client, backend Backend) (string, error) {
	body, err := proxyGetRaw(ctx, client, backend, "_cluster/health", map[string]string{"pretty": "false"})
	if err != nil {
		return "", err
	}
	resp := struct {
		Status string `json:"status"`
	}{}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.Status) == "" {
		return "unknown", nil
	}
	return strings.ToLower(strings.TrimSpace(resp.Status)), nil
}

func queryJaegerServices(ctx context.Context, client *kube.Client, backend Backend) ([]string, error) {
	paths := []string{"services"}
	for _, prefix := range backend.PathPrefixes {
		for _, suffix := range paths {
			body, err := proxyGetRaw(ctx, client, backend, joinProxyPath(prefix, suffix), nil)
			if err != nil {
				continue
			}
			resp := struct {
				Data []string `json:"data"`
			}{}
			if err := json.Unmarshal(body, &resp); err != nil {
				continue
			}
			sort.Strings(resp.Data)
			return resp.Data, nil
		}
	}
	return nil, fmt.Errorf("no jaeger services endpoint responded")
}

func queryJaegerOperations(ctx context.Context, client *kube.Client, backend Backend, service string) ([]string, error) {
	service = strings.TrimSpace(service)
	if service == "" {
		return nil, fmt.Errorf("service is empty")
	}
	for _, prefix := range backend.PathPrefixes {
		body, err := proxyGetRaw(ctx, client, backend, joinProxyPath(prefix, "services/"+service+"/operations"), nil)
		if err != nil {
			continue
		}
		resp := struct {
			Data []string `json:"data"`
		}{}
		if err := json.Unmarshal(body, &resp); err == nil {
			sort.Strings(resp.Data)
			return resp.Data, nil
		}
	}
	return nil, fmt.Errorf("no jaeger operations endpoint responded")
}

func queryTempoProbe(ctx context.Context, client *kube.Client, backend Backend) ([]string, error) {
	if _, err := proxyGetRaw(ctx, client, backend, "ready", nil); err == nil {
		signals := []string{"reachable /ready"}
		if services, err := queryTempoServices(ctx, client, backend); err == nil && len(services) > 0 {
			signals = append(signals, fmt.Sprintf("service-tag values=%d", len(services)))
		}
		return signals, nil
	}
	for _, prefix := range backend.PathPrefixes {
		body, err := proxyGetRaw(ctx, client, backend, joinProxyPath(prefix, "tags"), nil)
		if err != nil {
			continue
		}
		resp := struct {
			TagNames []string `json:"tagNames"`
		}{}
		if err := json.Unmarshal(body, &resp); err == nil {
			return []string{fmt.Sprintf("reachable trace search tags=%d", len(resp.TagNames))}, nil
		}
	}
	return nil, fmt.Errorf("tempo probe paths did not respond")
}

func queryTempoServices(ctx context.Context, client *kube.Client, backend Backend) ([]string, error) {
	paths := []string{
		"api/search/tag/service.name/values",
		"api/search/tag/service/values",
	}
	for _, path := range paths {
		body, err := proxyGetRaw(ctx, client, backend, path, nil)
		if err != nil {
			continue
		}
		resp := struct {
			TagValues []string `json:"tagValues"`
		}{}
		if err := json.Unmarshal(body, &resp); err == nil && len(resp.TagValues) > 0 {
			sort.Strings(resp.TagValues)
			return resp.TagValues, nil
		}
	}
	return nil, fmt.Errorf("tempo service values endpoint did not respond")
}

func detectLokiLabelPair(labels []string) (string, string) {
	return DetectNamespacePodLabelPair(labels)
}

func parseLokiValue(pair []any) (float64, bool) {
	if len(pair) < 2 {
		return 0, false
	}
	s, ok := pair[1].(string)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
