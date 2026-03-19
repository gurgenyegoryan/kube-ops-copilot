package telemetryruntime

import (
	"context"
	"fmt"
	"strings"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/capability"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/telemetry"
)

type Analyzer struct {
	Client    *kube.Client
	Inventory capability.Inventory
}

func (a *Analyzer) Name() string { return "telemetryruntime" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	res := analyzer.Result{}
	if a.Client == nil || a.Client.Kubernetes == nil {
		return res, fmt.Errorf("kubernetes client is nil")
	}

	backends, err := telemetry.Discover(ctx, a.Client, a.Inventory)
	if err != nil {
		return res, err
	}

	timeSeriesBackends := telemetry.PrometheusBackends(backends)
	logBackends := telemetry.LogsBackends(backends)
	traceBackends := telemetry.TracesBackends(backends)
	res.Evidence = append(res.Evidence, model.Evidence{
		Signal: fmt.Sprintf("telemetry runtime adapters discovered: timeSeries=%d logs=%d traces=%d", len(timeSeriesBackends), len(logBackends), len(traceBackends)),
	})

	for _, backend := range timeSeriesBackends {
		probe := telemetry.ProbePrometheusBackend(ctx, a.Client, backend)
		if probe.Reachable {
			res.Evidence = append(res.Evidence, model.Evidence{
				Signal: fmt.Sprintf("time-series backend reachable: vendor=%s service=%s/%s signals=%s", backend.Vendor, backend.Namespace, backend.Service, strings.Join(probe.Signals, "; ")),
			})
		}
		for _, unknown := range probe.Unknowns {
			res.Unknowns = append(res.Unknowns, unknown)
		}
	}

	for _, backend := range logBackends {
		probe := telemetry.ProbeLogsBackend(ctx, a.Client, backend)
		if probe.Reachable {
			res.Evidence = append(res.Evidence, model.Evidence{
				Signal: fmt.Sprintf("logs backend reachable: vendor=%s service=%s/%s signals=%s", backend.Vendor, backend.Namespace, backend.Service, strings.Join(probe.Signals, "; ")),
			})
			if probe.HealthStatus == "yellow" || probe.HealthStatus == "red" {
				res.Findings = append(res.Findings, model.Finding{
					Title:                 fmt.Sprintf("Logs backend %s/%s reports %s health", backend.Namespace, backend.Service, probe.HealthStatus),
					Severity:              healthSeverity(probe.HealthStatus),
					Urgency:               model.UrgencyToday,
					Confidence:            model.ConfidenceMedium,
					AffectedScope:         "platform/telemetry-runtime",
					WhyItMatters:          "When the logs backend itself is degraded, operator investigations become slower and cross-service failure chains are easier to miss.",
					AutomationSuitability: model.SuitabilityAdvisoryOnly,
				})
			}
			for _, sample := range probe.TopErrorPods {
				res.Evidence = append(res.Evidence, model.Evidence{
					Signal: fmt.Sprintf("logs error hotspot: %s/%s value=%.0f backend=%s", sample.Namespace, sample.Pod, sample.Value, backend.Vendor),
				})
			}
		}
		for _, unknown := range probe.Unknowns {
			res.Unknowns = append(res.Unknowns, unknown)
		}
	}

	for _, backend := range traceBackends {
		probe := telemetry.ProbeTracesBackend(ctx, a.Client, backend)
		if probe.Reachable {
			res.Evidence = append(res.Evidence, model.Evidence{
				Signal: fmt.Sprintf("traces backend reachable: vendor=%s service=%s/%s signals=%s", backend.Vendor, backend.Namespace, backend.Service, strings.Join(probe.Signals, "; ")),
			})
			if len(probe.TracedServices) > 0 {
				res.Evidence = append(res.Evidence, model.Evidence{
					Signal: fmt.Sprintf("traced services sample: %s", joinOrNone(limitStrings(probe.TracedServices, 8))),
				})
			}
		}
		for _, unknown := range probe.Unknowns {
			res.Unknowns = append(res.Unknowns, unknown)
		}
	}

	if a.Inventory["logs_backend"] != capability.NotConfirmed && len(logBackends) == 0 {
		res.Unknowns = append(res.Unknowns, "Logs backend capability was inferred, but no queryable logs service was discovered via Kubernetes Services in this run.")
	}
	if a.Inventory["traces_backend"] != capability.NotConfirmed && len(traceBackends) == 0 {
		res.Unknowns = append(res.Unknowns, "Traces backend capability was inferred, but no queryable traces service was discovered via Kubernetes Services in this run.")
	}
	if len(timeSeriesBackends) == 0 && len(logBackends) == 0 && len(traceBackends) == 0 {
		return res, nil
	}

	res.HiddenRisks = append(res.HiddenRisks, "Telemetry backends can exist but still be only partially queryable through cluster proxy paths; backend reachability should be treated as evidence, not assumed.")
	return res, nil
}

func healthSeverity(status string) model.Severity {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "red":
		return model.SeverityHigh
	case "yellow":
		return model.SeverityMedium
	default:
		return model.SeverityLow
	}
}

func limitStrings(in []string, limit int) []string {
	if limit <= 0 || len(in) <= limit {
		return in
	}
	return in[:limit]
}

func joinOrNone(in []string) string {
	if len(in) == 0 {
		return "none"
	}
	return strings.Join(in, ", ")
}
