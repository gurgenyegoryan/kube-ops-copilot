# Live External E2E

This directory contains the disposable-cluster E2E harness for Kube Ops Copilot.

What it does:

- creates or reuses a local `kind` cluster
- builds a tiny fake telemetry backend image from [cmd/e2e-telemetry-backend](../../cmd/e2e-telemetry-backend/main.go)
- deploys disposable Prometheus/Loki/Jaeger/Tempo-like services into the cluster
- deploys sample `payments` workloads that the analyzers can reason about
- runs opt-in Go tests with the `livee2e` build tag against the real cluster

Why this exists:

- fake client tests are fast and deterministic, but they do not fully exercise Kubernetes service proxy behavior
- this harness checks the real `diagnose` path against a disposable API server and disposable query backends
- it gives the project a stronger launch-quality confidence layer without making normal CI slow or flaky

## Local usage

Bring the cluster and stack up:

```bash
bash hack/e2e/kind-live.sh up
```

Run the live tests:

```bash
bash hack/e2e/kind-live.sh test
```

Inspect the cluster:

```bash
bash hack/e2e/kind-live.sh status
```

Delete the cluster:

```bash
bash hack/e2e/kind-live.sh down
```

## Files

- [Dockerfile.telemetry](./Dockerfile.telemetry)
- [kind/kustomization.yaml](./kind/kustomization.yaml)
- [kind/workloads.yaml](./kind/workloads.yaml)
- [kind/telemetry-backends.yaml](./kind/telemetry-backends.yaml)

## CI

The live workflow is defined in [live-e2e.yml](../../.github/workflows/live-e2e.yml).

It is intentionally separate from the normal CI workflow so that:

- regular PR checks stay fast
- live disposable-cluster checks can run on demand or on schedule
