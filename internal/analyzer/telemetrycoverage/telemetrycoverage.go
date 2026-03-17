package telemetrycoverage

import (
	"context"
	"fmt"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/capability"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
)

type Analyzer struct {
	Inventory capability.Inventory
}

func (a *Analyzer) Name() string { return "telemetrycoverage" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	_ = ctx
	res := analyzer.Result{}

	if len(a.Inventory) == 0 {
		res.Unknowns = append(res.Unknowns, "Capability inventory is empty; telemetry coverage could not be assessed.")
		return res, nil
	}

	res.Evidence = append(res.Evidence, model.Evidence{
		Signal: fmt.Sprintf("telemetry capability inventory: %s", capability.RenderInventory(filterCapabilities(a.Inventory, []string{
			"resource_metrics",
			"time_series_metrics",
			"logs_backend",
			"traces_backend",
			"telemetry_pipeline",
		}))),
	})

	if a.Inventory["resource_metrics"] == capability.NotConfirmed || a.Inventory["time_series_metrics"] == capability.NotConfirmed {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 "Metrics observability is only partially confirmed",
			Severity:              model.SeverityMedium,
			Urgency:               model.UrgencyThisWeek,
			Confidence:            model.ConfidenceMedium,
			AffectedScope:         "platform/telemetry",
			WhyItMatters:          "Without confirmed resource and trend metrics, the agent can catch Kubernetes symptoms but cannot reliably distinguish transient noise from real saturation or degradation trends.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})
	}

	if a.Inventory["logs_backend"] == capability.NotConfirmed {
		res.HiddenRisks = append(res.HiddenRisks, "Cluster-wide log aggregation is not confirmed; single-pod log sampling may miss multi-service failure chains.")
	}
	if a.Inventory["traces_backend"] == capability.NotConfirmed {
		res.Unknowns = append(res.Unknowns, "Distributed tracing backend is not confirmed; dependency-induced latency regressions may remain partially opaque.")
	}

	return res, nil
}

func filterCapabilities(in capability.Inventory, keys []string) capability.Inventory {
	out := capability.Inventory{}
	for _, k := range keys {
		if v, ok := in[k]; ok {
			out[k] = v
		}
	}
	return out
}
