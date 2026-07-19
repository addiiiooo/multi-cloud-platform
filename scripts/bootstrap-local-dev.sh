#!/usr/bin/env bash
#
# bootstrap-local-dev.sh
#
# Sets up a local Kubernetes cluster (via kind) for iterating on the
# platform's Kubernetes-facing work before any real EKS/GKE cluster exists,
# per the architecture's dev-environment design (a persistent EKS cluster
# alone costs more than the entire dev budget — see the architecture's Cost
# Optimization section).
#
# This script's job stops at "a cluster exists and the pulse-api image is
# loaded into it." It deliberately does NOT deploy any Kubernetes manifest
# for pulse-api — that Deployment/Service/HPA/Ingress definition is
# Milestone 11's shared Terraform/Helm module, not this milestone's.
# Running this script today just proves the local cluster and the image
# are ready for that module once it exists.
#
# kind (Kubernetes IN Docker) was chosen over k3d here because it's
# maintained directly under the kubernetes-sigs GitHub organization — the
# same governance as Kubernetes itself — which keeps this local
# approximation closer to upstream kube-apiserver/kubelet behavior than a
# k3s-based distribution would be. k3d remains a perfectly reasonable
# alternative if a team already standardizes on Rancher/k3s elsewhere; this
# repo just picks one rather than maintaining two parallel local-dev code
# paths for no engineering reason.
set -euo pipefail

CLUSTER_NAME="pulse-local"
IMAGE_TAG="pulse-api:local"

# Pinned rather than left at kind's default: the whole point of this
# cluster is to approximate the Kubernetes version Milestones 9-10 will
# actually run in EKS/GKE. Letting it silently float to whatever kind
# ships this month would let local dev quietly drift from staging/prod and
# turn "works on my kind cluster" into a false signal. Update this
# alongside whatever Kubernetes version gets chosen in Milestone 9/10,
# rather than independently of it.
KIND_NODE_IMAGE="kindest/node:v1.30.0"

log() { printf '\n[bootstrap-local-dev] %s\n' "$1"; }

require() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required tool: $1" >&2
    echo "$2" >&2
    exit 1
  fi
}

require docker  "Install Docker Desktop (or Docker Engine) first: https://docs.docker.com/get-docker/"
require kind    "Install kind first: https://kind.sigs.k8s.io/docs/user/quick-start/#installation"
require kubectl "Install kubectl first: https://kubernetes.io/docs/tasks/tools/#kubectl"

if ! docker info >/dev/null 2>&1; then
  echo "Docker is installed but the daemon isn't reachable — start Docker Desktop (or the Docker service) and try again." >&2
  exit 1
fi

if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
  log "Cluster '${CLUSTER_NAME}' already exists — skipping creation."
else
  log "Creating kind cluster '${CLUSTER_NAME}' (node image ${KIND_NODE_IMAGE})..."
  kind create cluster --name "${CLUSTER_NAME}" --image "${KIND_NODE_IMAGE}"
fi

kubectl config use-context "kind-${CLUSTER_NAME}"

log "Building ${IMAGE_TAG} from ./app ..."
docker build -t "${IMAGE_TAG}" -f app/Dockerfile app

log "Loading ${IMAGE_TAG} into ${CLUSTER_NAME}..."
kind load docker-image "${IMAGE_TAG}" --name "${CLUSTER_NAME}"

log "Done. Cluster '${CLUSTER_NAME}' is running and ${IMAGE_TAG} is loaded."
log "Nothing is deployed yet — Milestone 11 adds the Kubernetes manifests that run this image."
kubectl get nodes
