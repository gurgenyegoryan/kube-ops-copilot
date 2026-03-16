package pdb

import (
	"context"
	"fmt"
	"strings"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Analyzer struct {
	Client                  *kubernetes.Clientset
	IncludeSystemNamespaces bool
}

func (a *Analyzer) Name() string { return "pdb" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	if a.Client == nil {
		return analyzer.Result{}, fmt.Errorf("kubernetes client is nil")
	}
	res := analyzer.Result{}

	pdbs, err := a.Client.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list PDBs: %w", err)
	}
	res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("observed %d PodDisruptionBudget(s) (all namespaces)", len(pdbs.Items))})

	// This is intentionally conservative: we do not attempt full label selector matching against deployments
	// (which can be expensive and brittle without additional context). We flag low-signal risk based on
	// having very few PDBs outside system namespaces.
	userPDBs := 0
	for _, p := range pdbs.Items {
		if !a.IncludeSystemNamespaces && isSystemNamespace(p.Namespace) {
			continue
		}
		userPDBs++
		_ = p
	}

	if userPDBs == 0 {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 "No PodDisruptionBudgets detected outside system namespaces",
			Severity:              model.SeverityLow,
			Urgency:               model.UrgencyThisWeek,
			Confidence:            model.ConfidenceLow,
			AffectedScope:         "policies/pdb",
			WhyItMatters:          "Without PDBs, voluntary disruptions (node drain, upgrades) can evict too many pods at once, causing avoidable outages.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})

		res.Recommended.Preventive = append(res.Recommended.Preventive, model.Action{
			Title:               "Define PodDisruptionBudgets for critical multi-replica workloads",
			ExpectedBenefit:     "Reduces outage risk during node drains and cluster maintenance.",
			RiskTradeoff:        "Too-strict PDBs can block upgrades/drains; choose conservative budgets.",
			Priority:            "P3",
			ExecutionSafety:     model.SafetyNeedsOperatorApproval,
			ApprovalRequirement: model.ApprovalNeedsOperator,
			RollbackOutline:     "Remove or relax the PDB.",
		})

		res.Unknowns = append(res.Unknowns, "This check does not map PDBs to specific workloads; add workload-selector correlation for higher confidence.")
	}

	return res, nil
}

func isSystemNamespace(ns string) bool {
	s := strings.ToLower(ns)
	if s == "kube-system" || s == "kube-public" || s == "kube-node-lease" {
		return true
	}
	for _, p := range []string{"kube-", "amazon-", "aws-", "istio-", "ingress-", "cert-manager", "monitoring"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
