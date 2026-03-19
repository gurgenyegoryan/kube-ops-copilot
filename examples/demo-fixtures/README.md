# Demo Fixtures

These fixtures are meant to make the project easier to demo, benchmark, test, and share.

They cover representative production-style scenarios instead of toy examples:

1. `01-exposed-single-replica`
   Externally exposed Grafana-like workload running as a single replica without autoscaling.
2. `02-telemetry-blind-cluster`
   Cluster where observability coverage is not confirmed and policy defaults are missing.
3. `03-pending-pvc-storage-risk`
   Stateful workload degraded by pending PVCs and weak storage readiness.
4. `04-terragrunt-helm-remediation`
   Durable remediation path that belongs in Terraform/Terragrunt + Helm values instead of a live patch.
5. `05-time-series-correlated-hotspot`
   Workload where Prometheus-compatible telemetry confirms multi-hour restart and resource pressure on top of structural fragility.
6. `06-partial-telemetry-runtime`
   Cluster where logs/traces backends are reachable enough for runtime investigation, but telemetry completeness still has limits.
7. `07-n8n-approval-live-remediation`
   End-to-end approval-gated live remediation example suitable for Telegram/n8n review flows.
8. `08-smart-remediate-compound`
   End-to-end compound remediation example combining a live mitigation with a durable infra PR.

What is included:

- `diagnose-report.json`
  Deterministic report fixture shaped like the real `diagnose` JSON output.
- `diagnose.golden.md`
  Golden Markdown rendering for selected scenarios.
- `suggest.golden.txt`
  Example LLM response for `suggest`, including Markdown plus fenced JSON plan.
- `terraform-pr.golden.txt`
  Example LLM response for `terraform-pr`, including Markdown plus fenced JSON infra plan.

These fixtures are also used by tests and benchmarks so they stay useful as the project evolves.
