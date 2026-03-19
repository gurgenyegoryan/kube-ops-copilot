### Executive Summary
payments-api combines long-window restart and resource pressure with single-replica external exposure, making it the clearest near-term incident path in this snapshot.

### Snapshot Assessment
- production readiness score: 58/100
- operational risk: high
- automation confidence: medium
- observability coverage: confirmed
- top risk themes: workloads/time-series-correlation, workloads/runtime-metrics, workloads/autoscaling
- confidence limiters:
  - Prometheus-compatible telemetry is confirmed, but business saturation still needs service-level latency or queue context.
  - Statefulness and replica safety still need workload-specific verification before scale changes.

### Key Findings
- title: 1 workload(s) combine long-window runtime pressure with structural fragility
  severity: high
  urgency: immediate
  confidence: high
  affected scope: workloads/time-series-correlation
  why it matters: payments-api is not only structurally fragile; telemetry shows restart growth, CPU throttling, and sustained resource pressure on the same exposed single-replica workload.
  automation suitability: advisory_only
- title: 1 workload(s) are running hot against live CPU/memory requests
  severity: medium
  urgency: today
  confidence: high
  affected scope: workloads/runtime-metrics
  why it matters: Live resource pressure confirms the workload is already close to its runtime envelope and may degrade during a burst or rollout.
  automation suitability: advisory_only

### Evidence
- time-series adapter selected: vendor=thanos service=monitoring/thanos-query port=http
- time-series query selected for restarts: topk(10, sum by (namespace,pod) (increase(kube_pod_container_status_restarts_total[24h])))
- time-series query selected for throttling: sum by (namespace,pod) (rate(container_cpu_cfs_throttled_seconds_total{container!=\"\",pod!=\"\"}[5m]))
- time-series correlated hotspot: payments/Deployment/payments-api score=78 restarts24h=7 cpuPeak=1.820cores memoryPeak=1.54Gi throttlePeak=0.260 oom24h=1 services=1 externalServices=1 readyEndpoints=1 hasHPA=false hasPDB=false missingReadiness=1 missingLiveness=0 reasons=24h restart increase 7; 6h CPU peak 1.82 cores; 5m CPU throttling peak 0.260; 24h OOM terminations 1; externally exposed single replica; externally exposed without HPA; missing readiness on 1 container(s); restart growth overlaps with external single-replica exposure
- runtime hotspot: payments/Deployment/payments-api score=53 pods=1 cpu=930m cpuRequests=750m memory=882Mi memoryRequests=1Gi podsMissingCPURequests=0 podsMissingMemoryRequests=0 reasons=CPU usage is 124% of declared requests; memory usage is 86% of declared requests

### Likely Root Cause Hypotheses
- rank: 1
  probability: high
  description: The highest operational risk is concentrated in payments-api because long-window runtime pressure overlaps with exposed single-replica fragility and weak control-plane guardrails.
  supporting evidence:
    - time-series correlated hotspot: payments/Deployment/payments-api score=78 ...
  contradictory/missing evidence:
    - Need latency, queue, or business traffic context to decide whether request sizing or replica floor is the best first remediation.
  what to verify next:
    - Compare restart and throttling spikes to deployment history and node events.
    - Check whether payments-api can safely run more than one replica.
    - Validate whether request sizing or HPA is the better first durable fix.

### Recommended Actions
- immediate actions:
  - title: Investigate payments-api as the top correlated hotspot before scaling or restarting it
    expected benefit: Focuses operator effort on the workload already showing both structural weakness and long-window runtime pressure.
    risk/tradeoff: Scaling without checking statefulness, dependency bottlenecks, and request sizing may hide the wrong problem.
    priority: P0
    execution safety: safe_auto_candidate
    approval requirement: none
    rollback outline: N/A (read-only validation first)
- short-term actions:
  - title: Choose one durable control first for payments-api: request sizing, HPA floor, or replica floor
    expected benefit: Reduces the chance of repeated restart/throttle incidents without stacking multiple risky changes at once.
    risk/tradeoff: The wrong first change can increase cost or churn without addressing the dominant bottleneck.
    priority: P1
    execution safety: needs_operator_approval
    approval requirement: needs_operator_approval
    rollback outline: Revert the specific request/HPA/replica change if it worsens stability or cost.
- preventive improvements:
  - none

### Proposed Operator Message
Kube Ops Copilot: payments-api is the clearest incident path in this snapshot because long-window restart and CPU-throttling pressure overlap with exposed single-replica fragility. Best next step is to validate whether the first durable fix should be request sizing, HPA, or a replica floor before any live change.

### Hidden Risks / What Humans Might Miss
- A single snapshot might look acceptable because the pod is still Ready, while multi-hour restart and throttling drift is already visible in the time-series backend.

### Unknowns
- Business saturation and queue depth are not shown in this fixture, so the exact first remediation still needs workload context.

### Final Operator Verdict
Treat payments-api as a correlated hotspot and validate the smallest durable fix before any live mitigation.

_generated at 2026-03-19T09:30:00Z_
