### Executive Summary
Exposed single-replica Grafana without autoscaling is the clearest availability risk in this snapshot.

### Snapshot Assessment
- production readiness score: 71/100
- operational risk: high
- automation confidence: medium
- observability coverage: confirmed
- top risk themes: workloads/autoscaling, workloads/probes
- confidence limiters:
  - Probe behavior still needs workload-specific validation before any rollout.
  - Runtime telemetry is confirmed, but root-cause verification still needs live metrics and events.

### Key Findings
- title: Exposed single-replica workload without HPA: gosell-test/prometheus-grafana
  severity: high
  urgency: today
  confidence: high
  affected scope: workloads/autoscaling
  why it matters: A single exposed pod is a hard single point of failure during node drains, evictions, or rollout churn.
  automation suitability: approval_gated_change
- title: 9 container(s) missing readiness/liveness probes
  severity: medium
  urgency: this week
  confidence: high
  affected scope: workloads/probes
  why it matters: Without probes, Kubernetes can route traffic to unhealthy pods and self-healing becomes much less reliable.
  automation suitability: advisory_only

### Evidence
- singleReplicaExposedNoHPA=1 workload=gosell-test/prometheus-grafana
- missing probe(s): deploy gosell-test/prometheus-grafana container=grafana readiness=false liveness=false
- discovered platform API groups: monitoring.coreos.com, metrics.k8s.io
- service exposure inventory: ClusterIP=18 NodePort=0 LoadBalancer=1 ExternalName=0

### Likely Root Cause Hypotheses
- rank: 1
  probability: high
  description: The highest current availability risk comes from a statically scaled exposed workload rather than from an active crash loop.
  supporting evidence:
    - singleReplicaExposedNoHPA=1 workload=gosell-test/prometheus-grafana
  contradictory/missing evidence:
    - Need service and endpoint verification to confirm current traffic path and readiness.
  what to verify next:
    - Confirm the Service selects the Grafana pod and currently has ready endpoints.
    - Check whether the Grafana deployment can safely run more than one replica.

### Recommended Actions
- immediate actions:
  - title: Verify Grafana exposure path and scale from 1 to 2 replicas if the workload is stateless enough
    expected benefit: Removes the most obvious single point of failure without depending on probe correctness.
    risk/tradeoff: If the workload has singleton assumptions or storage constraints, scale-out may be unsafe until validated.
    priority: P1
    execution safety: needs_operator_approval
    approval requirement: needs_operator_approval
    rollback outline: Scale back to one replica and revert the deployment spec if multi-replica behavior is not safe.
- short-term actions:
  - none
- preventive improvements:
  - title: Add readiness and liveness probes for the affected workloads after validating endpoints and startup behavior
    expected benefit: Improves safe traffic routing and self-healing.
    risk/tradeoff: Incorrect probes can cause self-inflicted outages during rollout.
    priority: P2
    execution safety: needs_operator_approval
    approval requirement: needs_operator_approval
    rollback outline: Revert or relax probe configuration if rollout stability regresses.

### Proposed Operator Message
Kube Ops Copilot: exposed single-replica Grafana without autoscaling is the clearest availability risk in this snapshot. Best next action is to confirm the Service/endpoints path and, if safe, scale to two replicas with operator approval.

### Hidden Risks / What Humans Might Miss
- Probe omission may look harmless during calm periods but increases partial-failure risk during dependency incidents.
- A graceful-looking cluster can still be one node drain away from user-visible downtime when an exposed workload has only one replica.

### Unknowns
- Grafana multi-replica safety still needs validation against storage/session assumptions.

### Final Operator Verdict
Reduce the exposed Grafana single point of failure first, then address probe gaps as a staged hardening task.

_generated at 2026-03-18T10:00:00Z_
