package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
)

type VectorSample struct {
	Metric map[string]string
	Value  float64
}

type MatrixPoint struct {
	Timestamp time.Time
	Value     float64
}

type MatrixSeries struct {
	Metric map[string]string
	Values []MatrixPoint
}

type VectorQueryResult struct {
	Samples    []VectorSample
	Expression string
	PathPrefix string
}

type MatrixQueryResult struct {
	Series     []MatrixSeries
	Expression string
	PathPrefix string
}

type TimeSeriesProbe struct {
	Backend      Backend
	Reachable    bool
	BuildVersion string
	Signals      []string
	Unknowns     []string
}

type prometheusEnvelope struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string            `json:"resultType"`
		Result     []json.RawMessage `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

type prometheusVectorItem struct {
	Metric map[string]string `json:"metric"`
	Value  []any             `json:"value"`
}

type prometheusMatrixItem struct {
	Metric map[string]string `json:"metric"`
	Values [][]any           `json:"values"`
}

func PrometheusBackends(backends []Backend) []Backend {
	out := []Backend{}
	for _, backend := range backends {
		if backend.Kind == KindTimeSeries {
			out = append(out, backend)
		}
	}
	return out
}

func QueryPrometheusVector(ctx context.Context, client *kube.Client, backend Backend, expr string) ([]VectorSample, error) {
	res, err := QueryPrometheusVectorFirst(ctx, client, backend, []string{expr})
	if err != nil {
		return nil, err
	}
	return res.Samples, nil
}

func QueryPrometheusVectorFirst(ctx context.Context, client *kube.Client, backend Backend, expressions []string) (VectorQueryResult, error) {
	var lastErr error
	for _, expr := range expressions {
		expr = strings.TrimSpace(expr)
		if expr == "" {
			continue
		}
		for _, prefix := range backend.PathPrefixes {
			body, err := proxyGetRaw(ctx, client, backend, joinProxyPath(prefix, "query"), map[string]string{"query": expr})
			if err != nil {
				lastErr = err
				continue
			}
			result, err := decodeVector(body)
			if err == nil {
				return VectorQueryResult{Samples: result, Expression: expr, PathPrefix: prefix}, nil
			}
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no Prometheus-compatible path worked for %s/%s", backend.Namespace, backend.Service)
	}
	return VectorQueryResult{}, lastErr
}

func QueryPrometheusRange(ctx context.Context, client *kube.Client, backend Backend, expr string, start, end time.Time, step time.Duration) ([]MatrixSeries, error) {
	res, err := QueryPrometheusRangeFirst(ctx, client, backend, []string{expr}, start, end, step)
	if err != nil {
		return nil, err
	}
	return res.Series, nil
}

func QueryPrometheusRangeFirst(ctx context.Context, client *kube.Client, backend Backend, expressions []string, start, end time.Time, step time.Duration) (MatrixQueryResult, error) {
	params := map[string]string{
		"start": strconv.FormatFloat(float64(start.Unix()), 'f', 0, 64),
		"end":   strconv.FormatFloat(float64(end.Unix()), 'f', 0, 64),
		"step":  strconv.FormatFloat(step.Seconds(), 'f', 0, 64),
	}
	var lastErr error
	for _, expr := range expressions {
		expr = strings.TrimSpace(expr)
		if expr == "" {
			continue
		}
		params["query"] = expr
		for _, prefix := range backend.PathPrefixes {
			body, err := proxyGetRaw(ctx, client, backend, joinProxyPath(prefix, "query_range"), params)
			if err != nil {
				lastErr = err
				continue
			}
			result, err := decodeMatrix(body)
			if err == nil {
				return MatrixQueryResult{Series: result, Expression: expr, PathPrefix: prefix}, nil
			}
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no Prometheus-compatible query_range path worked for %s/%s", backend.Namespace, backend.Service)
	}
	return MatrixQueryResult{}, lastErr
}

func ProbePrometheusBackend(ctx context.Context, client *kube.Client, backend Backend) TimeSeriesProbe {
	probe := TimeSeriesProbe{Backend: backend}
	if version, prefix, err := queryPrometheusBuildInfo(ctx, client, backend); err == nil {
		probe.Reachable = true
		probe.BuildVersion = version
		if strings.TrimSpace(version) != "" {
			probe.Signals = append(probe.Signals, fmt.Sprintf("buildinfo version=%s path=%s", version, prefix))
		}
	} else {
		probe.Unknowns = append(probe.Unknowns, "buildinfo probe failed: "+strings.TrimSpace(err.Error()))
	}
	if result, err := QueryPrometheusVectorFirst(ctx, client, backend, []string{"up"}); err == nil {
		probe.Reachable = true
		probe.Signals = append(probe.Signals, fmt.Sprintf("queryable expr=%s path=%s samples=%d", result.Expression, result.PathPrefix, len(result.Samples)))
	} else {
		probe.Unknowns = append(probe.Unknowns, "prometheus-compatible query probe failed: "+strings.TrimSpace(err.Error()))
	}
	if backend.Vendor == "thanos" {
		if count, err := queryThanosStores(ctx, client, backend); err == nil {
			probe.Reachable = true
			probe.Signals = append(probe.Signals, fmt.Sprintf("thanos stores=%d", count))
		}
	}
	return probe
}

func queryPrometheusBuildInfo(ctx context.Context, client *kube.Client, backend Backend) (string, string, error) {
	type buildInfoResp struct {
		Status string `json:"status"`
		Data   struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	var lastErr error
	for _, prefix := range backend.PathPrefixes {
		body, err := proxyGetRaw(ctx, client, backend, joinProxyPath(prefix, "status/buildinfo"), nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp := buildInfoResp{}
		if err := json.Unmarshal(body, &resp); err != nil {
			lastErr = err
			continue
		}
		if resp.Status != "success" {
			lastErr = fmt.Errorf("unexpected buildinfo status=%s", resp.Status)
			continue
		}
		return resp.Data.Version, prefix, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("buildinfo path not reachable")
	}
	return "", "", lastErr
}

func queryThanosStores(ctx context.Context, client *kube.Client, backend Backend) (int, error) {
	type storeResp struct {
		Status string `json:"status"`
		Data   []any  `json:"data"`
	}
	paths := []string{"stores", "store"}
	var lastErr error
	for _, prefix := range backend.PathPrefixes {
		for _, suffix := range paths {
			body, err := proxyGetRaw(ctx, client, backend, joinProxyPath(prefix, suffix), nil)
			if err != nil {
				lastErr = err
				continue
			}
			resp := storeResp{}
			if err := json.Unmarshal(body, &resp); err != nil {
				lastErr = err
				continue
			}
			if resp.Status == "success" {
				return len(resp.Data), nil
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("thanos store path not reachable")
	}
	return 0, lastErr
}

func decodeVector(body []byte) ([]VectorSample, error) {
	env := prometheusEnvelope{}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	if env.Status != "success" {
		if strings.TrimSpace(env.Error) == "" {
			env.Error = "prometheus query failed"
		}
		return nil, fmt.Errorf("%s", env.Error)
	}
	if env.Data.ResultType != "vector" {
		return nil, fmt.Errorf("unexpected Prometheus result type: %s", env.Data.ResultType)
	}
	out := make([]VectorSample, 0, len(env.Data.Result))
	for _, raw := range env.Data.Result {
		item := prometheusVectorItem{}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		value, ok := parsePrometheusValue(item.Value)
		if !ok {
			continue
		}
		out = append(out, VectorSample{Metric: item.Metric, Value: value})
	}
	return out, nil
}

func decodeMatrix(body []byte) ([]MatrixSeries, error) {
	env := prometheusEnvelope{}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	if env.Status != "success" {
		if strings.TrimSpace(env.Error) == "" {
			env.Error = "prometheus range query failed"
		}
		return nil, fmt.Errorf("%s", env.Error)
	}
	if env.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("unexpected Prometheus result type: %s", env.Data.ResultType)
	}
	out := make([]MatrixSeries, 0, len(env.Data.Result))
	for _, raw := range env.Data.Result {
		item := prometheusMatrixItem{}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		series := MatrixSeries{Metric: item.Metric}
		for _, pair := range item.Values {
			value, ok := parsePrometheusValue(pair)
			if !ok || len(pair) == 0 {
				continue
			}
			ts, ok := parsePrometheusTimestamp(pair[0])
			if !ok {
				continue
			}
			series.Values = append(series.Values, MatrixPoint{Timestamp: ts, Value: value})
		}
		out = append(out, series)
	}
	return out, nil
}

func parsePrometheusValue(value []any) (float64, bool) {
	if len(value) < 2 {
		return 0, false
	}
	s, ok := value[1].(string)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func parsePrometheusTimestamp(v any) (time.Time, bool) {
	switch t := v.(type) {
	case float64:
		sec := int64(t)
		return time.Unix(sec, 0).UTC(), true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(int64(f), 0).UTC(), true
	default:
		return time.Time{}, false
	}
}

func joinProxyPath(prefix, suffix string) string {
	prefix = strings.TrimSpace(prefix)
	suffix = strings.Trim(strings.TrimSpace(suffix), "/")
	if prefix == "" || prefix == "/" {
		return suffix
	}
	return strings.Trim(strings.TrimRight(prefix, "/")+"/"+suffix, "/")
}
