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
