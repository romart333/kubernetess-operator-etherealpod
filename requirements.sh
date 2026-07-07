#!/usr/bin/env bash
# Bootstraps a local kind cluster with the EtherealPod operator and SundayApp.
# Requirements checked (not installed): docker, kubectl, kind.
set -euo pipefail

CLUSTER_NAME="sunday"
OPERATOR_IMAGE="etherealpod-operator:v0.1.0"
APP_IMAGE="sunday-app:v0.1.0"
CRD_NAME="etherealpods.workload.sunday.io"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

step() { printf '\n==> %s\n' "$*"; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

require() {
  local cmd="$1" url="$2"
  command -v "$cmd" >/dev/null 2>&1 \
    || die "'$cmd' is required but not installed. Install it: $url"
}

step "Checking prerequisites"
require docker "https://docs.docker.com/get-docker/"
require kubectl "https://kubernetes.io/docs/tasks/tools/"
require kind "https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
docker info >/dev/null 2>&1 || die "Docker daemon is not running."

step "Ensuring kind cluster '${CLUSTER_NAME}' exists"
if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
  echo "Cluster '${CLUSTER_NAME}' already exists — reusing it."
else
  kind create cluster --name "${CLUSTER_NAME}"
fi
kubectl config use-context "kind-${CLUSTER_NAME}" >/dev/null

step "Waiting for the node to be Ready"
kubectl wait node --all --for=condition=Ready --timeout=120s

step "Building images"
docker build -t "${OPERATOR_IMAGE}" "${SCRIPT_DIR}/operator"
docker build -t "${APP_IMAGE}" "${SCRIPT_DIR}/sundayapp"

step "Loading images into the kind cluster"
kind load docker-image "${OPERATOR_IMAGE}" --name "${CLUSTER_NAME}"
kind load docker-image "${APP_IMAGE}" --name "${CLUSTER_NAME}"

# Server-side apply: the EtherealPod CRD embeds the PodTemplateSpec schema
# and exceeds the 256KiB last-applied-configuration annotation limit of
# client-side apply.
step "Applying submission.yaml (first pass)"
# The CRD and a CR of its kind live in the same file; the first apply can
# race CRD establishment, so wait and apply once more (idempotent).
kubectl apply --server-side -f "${SCRIPT_DIR}/submission.yaml" || true

step "Waiting for the EtherealPod CRD to be established"
kubectl wait --for=condition=Established "crd/${CRD_NAME}" --timeout=60s

step "Applying submission.yaml (second pass)"
kubectl apply --server-side -f "${SCRIPT_DIR}/submission.yaml"

step "Waiting for the operator deployment to become Available"
kubectl wait -n etherealpod-system deploy/etherealpod-controller-manager \
  --for=condition=Available --timeout=180s

step "Smoke check: listing EtherealPods across namespaces"
kubectl get eps -A

step "Done"
echo "Try it:"
echo "  kubectl -n sunday get eps"
echo "  kubectl -n sunday port-forward svc/sunday-app 8080:80"
echo "  curl -X POST 'http://localhost:8080/write?user_id=loki&product_name=apple&amount=1'"
