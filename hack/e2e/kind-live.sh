#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CLUSTER_NAME="${KOC_E2E_CLUSTER_NAME:-kube-ops-copilot-e2e}"
IMAGE_TAG="${KOC_E2E_IMAGE_TAG:-kube-ops-copilot/e2e-telemetry:local}"
KUBECONFIG_PATH="${KOC_E2E_KUBECONFIG_PATH:-/tmp/${CLUSTER_NAME}-kubeconfig}"

usage() {
  cat <<'EOF'
Usage:
  hack/e2e/kind-live.sh up
  hack/e2e/kind-live.sh test
  hack/e2e/kind-live.sh down
  hack/e2e/kind-live.sh status

Environment:
  KOC_E2E_CLUSTER_NAME      kind cluster name
  KOC_E2E_IMAGE_TAG         fake telemetry image tag
  KOC_E2E_KUBECONFIG_PATH   kubeconfig path written for test runs
EOF
}

need_bin() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required binary: $1" >&2
    exit 1
  }
}

ensure_cluster() {
  if ! kind get clusters | grep -qx "${CLUSTER_NAME}"; then
    kind create cluster --name "${CLUSTER_NAME}"
  fi
  kind get kubeconfig --name "${CLUSTER_NAME}" > "${KUBECONFIG_PATH}"
  export KUBECONFIG="${KUBECONFIG_PATH}"
}

build_and_load_image() {
  docker build -f "${ROOT_DIR}/test/e2e/Dockerfile.telemetry" -t "${IMAGE_TAG}" "${ROOT_DIR}"
  kind load docker-image "${IMAGE_TAG}" --name "${CLUSTER_NAME}"
}

deploy_stack() {
  kubectl apply -k "${ROOT_DIR}/test/e2e/kind"
  kubectl rollout status -n monitoring deploy/prometheus --timeout=180s
  kubectl rollout status -n observability deploy/loki --timeout=180s
  kubectl rollout status -n observability deploy/jaeger-query --timeout=180s
  kubectl rollout status -n observability deploy/tempo --timeout=180s
  kubectl rollout status -n payments deploy/payments-api --timeout=180s
  kubectl rollout status -n payments statefulset/payments-worker --timeout=180s
}

run_tests() {
  export KUBECONFIG="${KUBECONFIG_PATH}"
  export KOC_LIVE_E2E=1
  (cd "${ROOT_DIR}" && go test -tags=livee2e ./internal/e2e -v)
}

status() {
  export KUBECONFIG="${KUBECONFIG_PATH}"
  kubectl get ns
  kubectl get pods -A -o wide
  kubectl get svc -A
}

main() {
  local cmd="${1:-}"
  case "${cmd}" in
    up)
      need_bin kind
      need_bin kubectl
      need_bin docker
      ensure_cluster
      build_and_load_image
      deploy_stack
      ;;
    test)
      need_bin kind
      need_bin kubectl
      need_bin go
      ensure_cluster
      run_tests
      ;;
    down)
      need_bin kind
      kind delete cluster --name "${CLUSTER_NAME}" || true
      rm -f "${KUBECONFIG_PATH}"
      ;;
    status)
      need_bin kubectl
      ensure_cluster
      status
      ;;
    *)
      usage
      exit 1
      ;;
  esac
}

main "$@"
