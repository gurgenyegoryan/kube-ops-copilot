# Kube Ops Copilot

Kube Ops Copilot is an approval-driven Kubernetes reliability copilot.

Its goal is not to print generic Kubernetes advice. Its goal is to inspect a real cluster, understand what capabilities actually exist, detect active and latent risks, and produce operator-grade recommendations with explicit safety boundaries.

It combines:

- deterministic Kubernetes analysis
- dynamic capability discovery
- LLM-assisted operator narrative
- approval-gated execution plans

## What makes this different

Most Kubernetes assistants assume too much:

- they assume Prometheus exists
- they assume Loki exists
- they assume HPA exists
- they assume ingress, GitOps, service mesh, tracing, cert management, or metrics pipelines are already present

This project is designed to do the opposite.

It first discovers what is actually visible in the cluster, then adapts its analysis. If a capability is not confirmed, it should say so clearly instead of hallucinating advice around tools that may not exist.

## Why People Switch From Generic AI To This

Generic AI Kubernetes answers often sound confident but miss the operational reality:

- they assume tooling that may not exist in your cluster
- they jump to “restart the pod” before separating symptom from cause
- they do not distinguish safe automation from approval-gated change
- they do not know when the right fix belongs in GitOps/Terraform instead of a live patch
- they rarely leave an audit trail that an SRE team would actually trust

Kube Ops Copilot is built for the opposite workflow:

- discover first, assume nothing
- explain confidence and confidence limiters explicitly
- propose the smallest safe remediation only when evidence supports it
- switch between live remediation and infrastructure PR mode based on the real fix path
- keep outputs shareable: operator message, execution plan, PR body, rollback notes, and verification checklist

## Core principles

- Evidence-first: every finding should be grounded in observed Kubernetes state.
- Dynamic discovery: the tool should infer what the cluster has instead of relying on hardcoded assumptions.
- Cause over symptom: recommendations should focus on likely root causes and hidden risks, not just visible failures.
- Safe execution: no impactful changes without explicit operator approval.
- Auditability: actions, approvals, and post-change verification should remain explicit.

## How it works

The analysis pipeline has two layers.

### 1. Base analyzers

These always run:

- cluster info
- cluster health
- events
- workloads
- resources
- workload risk profiling
- PDB checks
- discovery

### 2. Capability adapters

After capability discovery, the tool dynamically enables additional analyzers depending on what the cluster appears to support.

Current examples:

- telemetry coverage analyzer
- traffic exposure analyzer
- autoscaling posture analyzer
- policy coverage analyzer
- runtime resource metrics adapter for `metrics.k8s.io`
- API warning/deprecation capture from Kubernetes warning headers

This architecture is intended to grow. The point is that the agent should not be locked to a single observability stack or platform pattern.

Every report now also carries a heuristic snapshot assessment:

- production readiness score
- operational risk
- automation confidence
- observability coverage status
- confidence limiters
- top risk themes

This is meant to make each run easier to trust and easier to share with operators or stakeholders.

## What the tool tries to discover automatically

The discovery stage builds a capability inventory with states like:

- `detected`
- `candidate`
- `not_confirmed`

Examples of capability classes:

- `resource_metrics`
- `time_series_metrics`
- `logs_backend`
- `traces_backend`
- `telemetry_pipeline`
- `workload_autoscaling`
- `traffic_entrypoint`
- `network_segmentation`
- `dynamic_storage`
- `disruption_control`
- `certificate_management`
- `gitops`
- `secret_management`
- `service_mesh`

These are inferred from a mix of:

- Kubernetes API groups
- Services and exposed ports
- Pod-name heuristics
- HPAs, ingresses, StorageClasses, PVCs, NetworkPolicies, quotas, and related objects

This is deliberately capability-oriented, not vendor-oriented.

## Current kinds of problems it can surface

- NotReady nodes and pressure conditions
- Pending pods
- CrashLoopBackOff and restart churn
- likely OOM signals
- degraded deployments and stuck rollouts
- missing requests, limits, and probes
- missing or weak disruption controls
- pending PVCs
- externally exposed services without ready endpoints
- exposed single-replica workloads without autoscaling posture
- workloads where several weak signals combine into one real incident path
- live CPU/memory hotspots when Kubernetes resource metrics are confirmed
- active namespaces missing basic policy defaults
- observability coverage that is too weak to support confident production guidance

It also tries to surface hidden risks, for example:

- clusters that look healthy at rest but lack autoscaling
- workloads that are externally exposed but depend on a single replica
- workloads that individually look “fine” but together lack probes, PDB, HPA, and endpoint headroom
- telemetry gaps that make high-confidence suggestions impossible
- namespaces likely to accumulate noisy-neighbor incidents because they lack guardrails
- deprecated Kubernetes API usage that may break after a future cluster upgrade

## When this is actually production-ready

The honest answer: this project is already useful, but "production-ready" should mean more than "it runs."

For this tool, production-ready means all of the following are true:

- it can analyze unfamiliar clusters without assuming a specific vendor, observability stack, or repo layout
- it can explain confidence and confidence limiters clearly instead of sounding certain when data is incomplete
- it can rank real workload risk concentration, not just count isolated misconfigurations
- it can choose the right remediation path between read-only advice, live guarded execution, and infrastructure PR mode
- it leaves an audit trail: approval request, exact plan, rollback notes, verification steps, PR body, and result artifacts
- it degrades safely when telemetry, RBAC, or repo context is missing
- it is validated against realistic fixtures and CI, not only happy-path demos

What is already strong today:

- dynamic capability discovery instead of hardcoding Prometheus/Loki/GitOps assumptions
- workload-level structural risk profiling across Services, Ingress, EndpointSlice, HPA, PDB, probes, and resources
- runtime enrichment through the Kubernetes resource metrics API when `metrics.k8s.io` is confirmed
- live-vs-infra remediation selection
- Terraform/Terragrunt/OpenTofu-oriented PR execution flow with audit output
- live progress/activity rendering so operators can see what the agent is doing
- demo fixtures, golden outputs, and CI-covered report behavior

What still needs to mature before the project deserves a full "production-max" claim:

- deeper runtime adapters for confirmed logs/traces backends and richer long-window time-series analysis beyond `metrics.k8s.io` snapshots
- stronger repo mutation intelligence for complex Helm/Kustomize/Terragrunt graphs and richer cross-file edits
- more scenario coverage with regression tests for tricky real-world cases
- more end-to-end verification around approval workflows, notifications, and PR automation against external systems

That is the bar this project should be judged against. The goal is not generic Kubernetes advice. The goal is a reliable operator agent that still behaves well when the cluster, tooling, and infrastructure layout are unfamiliar.

## Install

### From source

```bash
go test ./...
go build ./cmd/kube-ops-copilot
./kube-ops-copilot version
```

### Via Makefile

```bash
make test
make build VERSION=dev
./bin/kube-ops-copilot version
```

### Container image

Releases publish to GHCR:

```bash
docker pull ghcr.io/<org>/<repo>:vX.Y.Z
```

## Quick start

Long-running commands now show live progress in the terminal.

- interactive terminals get a single refreshing status line
- long phases such as cluster analysis, LLM planning, approval waiting, validation, git push, and PR creation update in place
- the terminal now keeps a rolling activity feed under the live status line, so you can see what the agent is doing in the background instead of waiting on a silent spinner
- durable events like approval ids, plan paths, and final results are still printed as normal lines
- infra planning flows also write a repo inventory snapshot to `/tmp`, so when a Terraform PR plan is `null` you can inspect exactly what files and links the agent analyzed

### Demo fixtures and golden outputs

Launch-ready examples live in [examples/demo-fixtures](/home/gurgen/projects/personal/k8s-aiagent/examples/demo-fixtures/README.md).

They include:

- 4 production-style scenario fixtures
- golden `diagnose` Markdown output
- golden `suggest` response with fenced execution plan
- golden `terraform-pr` response with fenced infra PR plan

These fixtures are also covered by tests and benchmarks so they stay useful as the project evolves.

### 1. Deterministic diagnosis

```bash
# Uses in-cluster config when available, otherwise $KUBECONFIG or ~/.kube/config
./kube-ops-copilot diagnose --output markdown
```

Explicit context:

```bash
./kube-ops-copilot diagnose \
  --kubeconfig ~/.kube/config \
  --context prod \
  --output markdown
```

Useful flags:

- `--events-since 2h`
- `--include-system-namespaces`
- `--notify`

### 2. LLM-assisted suggestions

`suggest` runs the deterministic analyzers first, then asks an LLM to turn the evidence into:

- triage order
- likely root causes
- production-readiness gaps
- next read-only verification steps
- one best remediation or improvement

Example:

```bash
export OPENAI_API_KEY=...

./kube-ops-copilot suggest \
  --llm-provider openai \
  --llm-model gpt-4.1-mini \
  --kubeconfig ~/.kube/config \
  --context prod
```

Generate a machine-readable execution plan when possible:

```bash
./kube-ops-copilot suggest \
  --llm-provider openai \
  --llm-model gpt-4.1-mini \
  --kubeconfig ~/.kube/config \
  --context prod \
  --plan-out /tmp/kube-ops-copilot-plan.json
```

Important behavior:

- if the cluster has no confirmed telemetry backend, the tool should say that clearly
- if a capability is only weakly inferred, the narrative should treat it as a candidate, not a fact
- if the best recommendation is not executable by the current plan schema, the Markdown can still recommend it while the JSON plan remains `null`

### 3. Approval-gated remediation flow

This is the higher-level flow:

1. diagnose
2. LLM proposes one best plan, if safe
3. approval request is sent
4. the tool waits for approval
5. the plan is executed
6. the result is verified

Example with n8n:

```bash
export OPENAI_API_KEY=...
export KUBE_OPS_COPILOT_N8N_WEBHOOK_URL='https://<your-n8n>/webhook/koc-approval'
export KUBE_OPS_COPILOT_N8N_BEARER_TOKEN='optional-shared-secret'

./kube-ops-copilot remediate \
  --llm-provider openai \
  --llm-model gpt-4.1-mini \
  --kubeconfig ~/.kube/config \
  --context prod \
  --approval-provider n8n \
  --notify \
  --wait-approval \
  --apply
```

Important:

- `--approval-provider n8n` controls how approval is requested and checked
- `--notify` controls whether the CLI also sends a notification through the configured notifier stack
- if you want Telegram notifications directly from the CLI, you still need `--notify` plus `KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN` and `KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID`
- if your n8n workflow itself sends Telegram messages, that is separate from the CLI notifier

### 4. Terraform PR mode

If your infrastructure is managed in Terraform and you give the tool access to that repo, it can take a GitOps-style remediation path:

1. analyze the live cluster
2. decide that the durable fix belongs in Terraform
3. generate a Terraform PR plan
4. request approval
5. edit Terraform files locally
6. run `terraform fmt`
7. create a branch and commit
8. optionally push and open a GitHub PR

Basic example:

```bash
./kube-ops-copilot terraform-pr \
  --llm-provider openai \
  --llm-model gpt-5.2 \
  --kubeconfig ~/.kube/config \
  --context prod \
  --infra-repo-path ~/infra/live/prod \
  --approval-provider n8n \
  --wait-approval \
  --apply
```

Push branch and open PR:

```bash
./kube-ops-copilot terraform-pr \
  --llm-provider openai \
  --llm-model gpt-5.2 \
  --kubeconfig ~/.kube/config \
  --context prod \
  --infra-repo-path ~/infra/live/prod \
  --approval-provider n8n \
  --wait-approval \
  --apply \
  --git-push \
  --open-pr \
  --base-branch main
```

Important:

- this mode is Terraform-first, and the generic infra layer also supports `opentofu` and `terragrunt` plans
- it edits only files present in the sampled repo inventory sent to the LLM
- repo narrowing is dependency-aware: the inventory now includes detected stacks, local module sources, terragrunt dependencies/includes, and higher-scored candidate files
- local reference expansion is layout-agnostic: when wrapper files point to chart paths, values files, templates, manifests, Helmfile/Kustomize targets, shell entrypoints, or other repo-local targets, the inventory tries to pull those linked files in automatically instead of stopping at the wrapper layer
- HCL resolution is now hybrid: the agent first tries a real HCL2 parser/evaluator layer for `locals`, `include`, `dependency`, `source`, and path-like attributes, then falls back to heuristics only when structural evaluation is inconclusive
- Terragrunt evaluation goes deeper than raw string parsing: the resolver now understands `read_terragrunt_config(...)` and can carry `locals` and `inputs` through chained config files when that is required to find real chart paths or `values-<env>.yaml` files
- the repo graph is multi-file, not HCL-only: it also follows Helm charts, Helmfile, Kustomize overlays, raw Kubernetes YAML, and command-style references such as `kubectl -f/-k`, `helm -f`, and `kustomize build`
- repo narrowing is also semantic, not only structural: cluster findings like namespace/workload names are turned into environment and workload aliases so the inventory can prioritize files such as `environments/test/...`, `modules/.../prometheus`, or `helm-charts/temporalio/...` even in unfamiliar layouts
- it prefers HCL-aware edits like `hcl_set_attribute`, `hcl_delete_attribute`, `hcl_replace_block`, and `hcl_append_block_body`, with literal search/replace kept as fallback
- HCL-aware attribute updates are multiline-friendly, so the executor can safely replace nested maps/lists instead of only one-line scalar values
- by default it refuses to work in a dirty git repo
- it does not run `terraform apply`
- validation is backend-aware: Terraform/OpenTofu validate changed module directories, and Terragrunt uses `hclvalidate` plus `validate-inputs` where a local stack is detected
- generated PR bodies include a change summary, changed files, verification steps, risk matrix, post-merge checklist, and rollback guidance

Useful flags:

- `--infra-repo-path`
- `--git-push`
- `--open-pr`
- `--base-branch`
- `--terraform-validate`
- `--require-clean-repo`
- `--plan-out`

### 5. Smart unified remediation

`smart-remediate` is the command that chooses the remediation path:

- live Kubernetes action via `ExecutionPlan`
- infra pull request via `InfraPRPlan`
- or `null` if neither is justified

Example:

```bash
./kube-ops-copilot smart-remediate \
  --llm-provider openai \
  --llm-model gpt-5.2 \
  --kubeconfig ~/.kube/config \
  --context prod \
  --infra-repo-path ~/infra/live/prod \
  --approval-provider n8n \
  --wait-approval \
  --apply \
  --git-push \
  --open-pr \
  --base-branch main
```

Use this when you want the agent to decide whether the right fix is:

- an immediate cluster change
- a GitOps/Terraform PR
- a compound plan: immediate live mitigation plus durable infra PR
- or investigation only

When a compound plan is applied, the tool also writes a lightweight orchestration trace next to the plan file:

- `<plan>.compound-result.json` records phase state for `live` and `infra`
- each phase has `pending`, `running`, `succeeded`, `failed`, or `skipped`
- this makes it easier to understand whether the live mitigation succeeded before the repo branch/PR step started

### 6. Infra execute and status

You can also execute or inspect an infra plan directly.

Execute an approved infra plan:

```bash
./kube-ops-copilot infra execute \
  --plan /tmp/kube-ops-copilot-terraform-plan.json \
  --infra-repo-path ~/infra/live/prod \
  --approval-provider n8n \
  --approval-id APPROVAL-123 \
  --wait-approval \
  --apply \
  --git-push \
  --open-pr
```

Inspect local status:

```bash
./kube-ops-copilot infra status \
  --plan /tmp/kube-ops-copilot-terraform-plan.json \
  --infra-repo-path ~/infra/live/prod
```

## Infra planning details

The infrastructure planning path is intentionally conservative.

### Dependency-aware repo narrowing

Before the LLM is asked to propose a repo change, the tool builds a repo inventory that tries to answer:

- which files look most relevant to the current cluster findings
- which directories behave like stacks
- which Terraform modules point at local sources
- which Terragrunt files reference other stacks via `dependency`, `include`, or `terraform.source`
- which Terragrunt configs compute their final targets through chained `locals`, `inputs`, and `read_terragrunt_config(...)`
- which local path/file/chart/values references lead to the actual editable YAML, Helm, Helmfile, Kustomize, or template files
- which shell or CI entrypoints point at those same files through `kubectl`, `helm`, `helmfile`, `kustomize`, `terraform`, `tofu`, or `terragrunt` commands
- which repo paths are semantically close to the live cluster findings based on workload names, namespaces, and inferred environment names

That inventory is then sampled and sent to the model instead of dumping the entire repository blindly.

### Supported HCL-aware edit types

Current structured edit types:

- `hcl_set_attribute`
- `hcl_delete_attribute`
- `hcl_replace_block`
- `hcl_append_block_body`
- `search_replace` as fallback

These are intended for precise repo updates such as:

- changing replica defaults in a module
- removing an unsafe attribute
- replacing a whole resource or module block
- appending a lifecycle stanza, tags block, or nested configuration block

Example `InfraPRPlan` snippet:

```json
{
  "kind": "InfraPRPlan",
  "backend": "terraform",
  "summary": "Raise Grafana replica count in the prod monitoring module",
  "edits": [
    {
      "type": "hcl_set_attribute",
      "path": "monitoring/grafana.tf",
      "blockType": "module",
      "labels": ["grafana"],
      "attribute": "replicas",
      "valueHCL": "2"
    }
  ]
}
```

For nested expressions, `valueHCL` may also be multiline HCL:

```hcl
{
  enabled = true
  paths = [
    "/readyz",
    "/healthz",
  ]
}
```

### Backend-aware validation behavior

The executor validates differently depending on the chosen backend:

- Terraform: `terraform fmt -recursive` plus `terraform validate` in changed module directories when possible
- OpenTofu: `tofu fmt -recursive` plus `tofu validate`
- Terragrunt: `terragrunt hclfmt`, then `terragrunt hclvalidate` and `terragrunt validate-inputs` in changed stack directories

This is intentionally more wrapper-aware than a single repo-root validate command, but still conservative enough to avoid assuming a custom wrapper that may not exist.

### Smarter PR generation

Generated PR bodies now try to be operator-friendly, not just git-friendly.

They include:

- the requested change summary
- changed files
- verification checklist
- verification notes
- risk matrix
- post-merge checklist
- rollback guidance

This makes the PR readable for platform engineers who were not present when the remediation was proposed.

### 7. Execute an approved plan manually

Execution is dry-run by default.

```bash
./kube-ops-copilot execute \
  --plan examples/execute-restart-deployment.json \
  --approval-id TICKET-123 \
  --approve \
  --dry-run
```

Actually apply:

```bash
./kube-ops-copilot execute \
  --plan examples/execute-restart-deployment.json \
  --kubeconfig ~/.kube/config \
  --context prod \
  --approval-id TICKET-123 \
  --approve \
  --dry-run=false
```

## Example operator scenarios

### Scenario: cluster looks healthy, but suggestions are weak

Run:

```bash
./kube-ops-copilot diagnose --context prod --output markdown
```

What you may see:

- no immediate critical incident
- `resource_metrics=not_confirmed`
- `time_series_metrics=not_confirmed`
- `logs_backend=not_confirmed`

Interpretation:

The cluster may be operational, but the agent will correctly lower its confidence because it cannot confirm enough telemetry coverage to reason about trends and pre-incident drift.

### Scenario: exposed service, but no backends

The traffic exposure analyzer may flag:

- externally exposed services with zero ready endpoints

That usually points to things like:

- selector drift
- readiness failures
- failed rollout
- no matching pods

This is a good example of a real production problem the tool can catch without needing Prometheus or Loki.

### Scenario: single replica behind public traffic

The autoscaling posture analyzer may flag:

- exposed deployment
- single replica
- no HPA

That is not necessarily an active outage, but it is a very real production risk that can turn into one during node drains, restarts, or traffic bursts.

## Notifications

`diagnose`, `suggest`, and `execute` can be wired into Slack, Telegram, or n8n-based workflows depending on your setup.

Recommended production pattern in this repo:

- CLI or automation calls Kube Ops Copilot
- notifications and approvals go through n8n
- n8n forwards to Telegram or Slack

Example:

```bash
export KUBE_OPS_COPILOT_N8N_WEBHOOK_URL='https://<your-n8n>/webhook/koc-approval'
export KUBE_OPS_COPILOT_N8N_BEARER_TOKEN='optional-shared-secret'

./kube-ops-copilot diagnose --notify ...
./kube-ops-copilot execute --notify ...
```

## LLM providers

Supported providers for `suggest`:

- OpenAI
- Anthropic
- Ollama

Examples:

```bash
./kube-ops-copilot suggest --llm-provider openai --llm-model gpt-4.1-mini
./kube-ops-copilot suggest --llm-provider anthropic --llm-model claude-3-5-sonnet-latest
./kube-ops-copilot suggest --llm-provider ollama --llm-model llama3.1
```

Environment variables:

- OpenAI: `KUBE_OPS_COPILOT_OPENAI_API_KEY` or `OPENAI_API_KEY`
- Anthropic: `KUBE_OPS_COPILOT_ANTHROPIC_API_KEY` or `ANTHROPIC_API_KEY`
- Ollama: set `--llm-base-url` if needed

## Approval workflows

`execute` always requires:

- `--approve`
- `--approval-id <id>`

Optionally, you can also require an external approval provider before any change is applied.

### Manual provider

```bash
./kube-ops-copilot execute \
  --approval-provider manual \
  --approval-id TICKET-123 \
  --approve \
  --plan examples/execute-restart-deployment.json
```

### n8n provider

Configuration:

```bash
export KUBE_OPS_COPILOT_N8N_WEBHOOK_URL='https://<your-n8n>/webhook/koc-approval'
export KUBE_OPS_COPILOT_N8N_BEARER_TOKEN='optional-shared-secret'
```

Approval request payload:

```json
{"action":"request","approvalId":"...","summary":"...","details":"...","operation":"...","target":"...","planPath":"..."}
```

Status payload:

```json
{"action":"status","approvalId":"..."}
```

Expected response:

```json
{"approvalId":"...","decision":"approved|denied|pending","approver":"...","reason":"...","raw":{}}
```

End-to-end example:

```bash
./kube-ops-copilot approval request \
  --provider n8n \
  --plan examples/execute-restart-deployment.json \
  --write-plan

./kube-ops-copilot execute \
  --plan examples/execute-restart-deployment.json \
  --approval-provider n8n \
  --approval-id <approval-id> \
  --approve \
  --dry-run=false \
  --wait-approval
```

Ready-to-import workflow:

- [examples/n8n-approval-workflow.json](examples/n8n-approval-workflow.json)

Expected n8n environment variables:

- `KOC_PUBLIC_BASE_URL`
- `KOC_DECISION_SECRET`
- optional `KOC_N8N_BEARER_TOKEN`
- `KOC_TELEGRAM_BOT_TOKEN`
- `KOC_TELEGRAM_CHAT_ID`

### Telegram provider

The Telegram approval flow treats replies like:

- `approve <approval-id>`
- `deny <approval-id>`

Configuration:

```bash
export KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN='123456:ABC...'
export KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID='-1001234567890'
```

Example:

```bash
id=$(./kube-ops-copilot approval request --provider telegram --plan examples/execute-restart-deployment.json --write-plan | awk '{print $NF}')

./kube-ops-copilot execute \
  --plan examples/execute-restart-deployment.json \
  --approval-provider telegram \
  --approval-id "$id" \
  --approve \
  --wait-approval \
  --dry-run=false
```

### Slack provider

Slack support exists in the codebase, though the recommended production path in this repo remains n8n-based orchestration.

## Limitations

Current limitations are important:

- capability detection is still heuristic in parts
- the tool does not yet dynamically query every detected telemetry backend
- some findings are still Kubernetes-native rather than full cross-signal correlation
- absence of evidence is not proof of absence
- HCL-aware edits currently focus on setting top-level block attributes and still fall back to literal search/replace for harder cases
- Terraform PR mode assumes plain `git`, optional `gh`, and plain `terraform` / `tofu` / `terragrunt` workflows
- very large or indirect Terraform repos may require passing a narrower `--infra-repo-path`
- compound remediation is supported, but only as a sequential live-then-infra flow inside one plan

In other words: this project is already designed to avoid shallow hardcoded advice, but it is still evolving toward deeper multi-backend runtime analysis.

## Development

Run tests:

```bash
GOCACHE=/tmp/gocache go test ./...
```

Lint:

```bash
make lint
```

## Release notes

Tagging `vX.Y.Z` triggers the release workflow:

- multi-arch binaries via GoReleaser
- GitHub Release assets and checksums
- SBOM generation
- keyless `cosign` signing for checksums and container image
