# Implementation Milestones — Multi-Cloud IaC Deployment Platform

Derived from the approved architecture (`multi-cloud-iac-platform-architecture.md`), Sections 5, 7, 8, 10, 11. The architecture itself is unchanged; the notes below only pin down file-tree details that section left implicit:

- **Container registries** (ECR, Artifact Registry) get their own `terraform/global/registries/` root, alongside `global/bootstrap/` — they're account-level singletons like the state backend, not per-environment resources.
- **GitHub-facing OIDC** (CI/CD deploy credentials) gets its own modules — `modules/aws/github-oidc/`, `modules/gcp/github-oidc/` — separate from **pod-facing** `iam-irsa` / `iam-workload-identity`. Same OIDC concept, two different trust relationships; conflating them in one module would make the trust policies harder to reason about.
- Helm values files are named `values-{env}-{cloud}.yaml` (not just `values-{env}.yaml`), since ingress annotations differ by cloud even within the same environment.
- The app is `pulse-api`, written in Go, packaged as a Helm chart, applied via Terraform's `helm_release` resource (a concrete choice the architecture doc left open) — this keeps deploys Terraform-orchestrated while leaving the chart usable standalone in an emergency.

**How to read the tracks below:** milestones inside the same track were designed to be built in either order or in parallel — the split exists specifically to satisfy "minimize dependencies." Cross-track dependencies are named explicitly per milestone.

---

## Track A — Foundations (no cloud dependency)

### Milestone 0 — Repository & Tooling Scaffolding
**Depends on:** nothing.

- **Goal:** Stand up the repo skeleton, review/branch rules, and local tooling so every later milestone has somewhere to land.
- **Files to create:** `README.md`, `SECURITY.md`, `.pre-commit-config.yaml`, `.github/CODEOWNERS`, `.github/pull_request_template.md`, `docs/adr/0001-record-architecture-decisions.md`, placeholder `.gitkeep` files under `terraform/`, `app/`, `helm/`, `scripts/`, `docs/runbooks/`.
- **Services involved:** None (GitHub only).
- **Expected output:** A cloned repo with branch protection on `main` (required reviews + status checks, even with no checks defined yet) and pre-commit hooks installable.
- **Verify:** `pre-commit run --all-files` exits 0 on the empty scaffold; attempting a direct push to `main` is rejected by branch protection.

### Milestone 1 — Containerize `pulse-api` & Local Dev Loop
**Depends on:** M0.

- **Goal:** A working stateless Go service with the three endpoints the whole architecture's health/readiness/observability story depends on, runnable with zero cloud dependency.
- **Files to create:** `app/src/main.go` (+ handlers), `app/tests/*_test.go`, `app/Dockerfile` (multi-stage, distroless, non-root), `docker-compose.yml`, `scripts/bootstrap-local-dev.sh` (spins up `kind`/`k3d` for local AWS-side iteration per the architecture's dev-environment design).
- **Services involved:** None.
- **Expected output:** A container image running locally that serves `/healthz`, `/readyz`, `/metrics`, and one real JSON route.
- **Verify:** `docker build` succeeds; `docker compose up` + `curl localhost:8080/healthz` returns `200`; `go test ./...` passes; image size and non-root user confirmed via `docker inspect`.

---

## Track B — Remote State & Registries (parallel AWS/GCP)

### Milestone 2 — Terraform Remote State Bootstrap: AWS
**Depends on:** M0. **Parallel with:** M3.

- **Goal:** Create the one thing every other AWS Terraform root needs before it can run: a remote backend.
- **Files to create:** `terraform/global/bootstrap/aws_backend.tf`, `terraform/global/bootstrap/README.md`.
- **Services involved:** S3, DynamoDB, IAM.
- **Expected output:** An S3 bucket (versioned, SSE-KMS encrypted, public access blocked) and a DynamoDB lock table, applied once with local state.
- **Verify:** `aws s3api get-bucket-versioning` shows `Enabled`; a test `terraform init` from a throwaway root using this backend succeeds and creates a lock row in DynamoDB during apply.

### Milestone 3 — Terraform Remote State Bootstrap: GCP
**Depends on:** M0. **Parallel with:** M2.

- **Goal:** Same as M2, GCP side.
- **Files to create:** `terraform/global/bootstrap/gcp_backend.tf` (same README, appended).
- **Services involved:** GCS, IAM.
- **Expected output:** A GCS bucket (versioned, uniform bucket-level access, CMEK-encrypted).
- **Verify:** `gsutil versioning get` shows enabled; a throwaway root's `terraform init -backend-config=...` succeeds against this bucket.

### Milestone 4 — Container Registry: ECR
**Depends on:** M2. **Parallel with:** M5.

- **Goal:** Somewhere for the CI pipeline to push AWS-side images, with scan-on-push and a lifecycle policy so untagged images don't accumulate.
- **Files to create:** `terraform/modules/aws/ecr/{main.tf,variables.tf,outputs.tf}`, `terraform/global/registries/aws.tf`.
- **Services involved:** ECR, IAM.
- **Expected output:** One ECR repository for `pulse-api` with scan-on-push enabled and a 30-day/last-10-tags lifecycle rule.
- **Verify:** `docker push` a test tag manually with local AWS creds; confirm it appears in the console with a completed vulnerability scan; confirm an 11th pushed tag causes the oldest to be pruned per the lifecycle rule.

### Milestone 5 — Container Registry: Artifact Registry
**Depends on:** M3. **Parallel with:** M4.

- **Goal:** GCP-side equivalent of M4.
- **Files to create:** `terraform/modules/gcp/artifact-registry/{main.tf,variables.tf,outputs.tf}`, `terraform/global/registries/gcp.tf`.
- **Services involved:** Artifact Registry, IAM.
- **Expected output:** One Artifact Registry Docker repository with vulnerability scanning enabled and a cleanup policy.
- **Verify:** `gcloud auth configure-docker` + manual push succeeds; repository shows the scan result; cleanup policy dry-run reports the expected candidates.

---

## Track C — CI Build Pipeline

### Milestone 6 — CI: Build, Test, Scan, Dual-Push
**Depends on:** M1, M4, M5.

- **Goal:** One pipeline, triggered on PR and on merge to `main`, that proves the "build once, push to both clouds" claim end to end — before any cluster exists to deploy to.
- **Files to create:** `.github/workflows/ci-app.yml`, `scripts/smoke-test.sh` (stubbed — expanded in M18).
- **Services involved:** ECR, Artifact Registry, IAM/OIDC (GitHub → AWS and GitHub → GCP for push-only permissions).
- **Expected output:** On PR: lint, unit tests, Trivy scan, build (no push). On merge: the same image, tagged with the Git SHA, pushed to both registries.
- **Verify:** Open a PR — checks run and block merge on failure. Merge it — confirm the identical image digest exists in both ECR and Artifact Registry under the same Git-SHA tag (`docker manifest inspect` digest comparison).

---

## Track D — Networking (parallel AWS/GCP)

### Milestone 7 — AWS Networking (VPC/Subnets/NAT) — Staging
**Depends on:** M2. **Parallel with:** M8.

- **Goal:** The network foundation EKS will sit inside — public subnets for the ALB/NAT, private subnets for nodes, across 3 AZs.
- **Files to create:** `terraform/modules/aws/network/{main.tf,variables.tf,outputs.tf}`, `terraform/environments/staging/aws/{main.tf,backend.tf,variables.tf,terraform.tfvars}`.
- **Services involved:** VPC, NAT Gateway, IAM.
- **Expected output:** A staging VPC with 3 public + 3 private subnets, one NAT gateway, route tables wired correctly, module outputs (`vpc_id`, `private_subnet_ids`, `public_subnet_ids`) matching the parallel contract GCP's module will also expose.
- **Verify:** `terraform apply` succeeds; a test EC2 instance in a private subnet can reach the internet via NAT but is not reachable from the internet directly; `terraform plan` afterward shows zero drift.

### Milestone 8 — GCP Networking (VPC/Subnets/Cloud NAT) — Staging
**Depends on:** M3. **Parallel with:** M7.

- **Goal:** GCP analog of M7 — VPC-native subnet with secondary ranges for pods/services, ready for a GKE cluster.
- **Files to create:** `terraform/modules/gcp/network/{main.tf,variables.tf,outputs.tf}`, `terraform/environments/staging/gcp/{main.tf,backend.tf,variables.tf,terraform.tfvars}`.
- **Services involved:** VPC, Cloud NAT, IAM.
- **Expected output:** A staging VPC with a regional subnet, pod/service secondary ranges sized for the expected staging pod count, Cloud NAT and Private Google Access enabled. Outputs match M7's contract shape.
- **Verify:** `terraform apply` succeeds; a private test VM reaches the internet via Cloud NAT and reaches Artifact Registry via Private Google Access without a public IP; `terraform plan` shows zero drift.

---

## Track E — Kubernetes Clusters (parallel AWS/GCP)

### Milestone 9 — AWS EKS Cluster — Staging
**Depends on:** M7. **Parallel with:** M10.

- **Goal:** A real, reachable Kubernetes control plane with an OIDC provider ready for IRSA, and managed node groups.
- **Files to create:** `terraform/modules/aws/eks-cluster/{main.tf,variables.tf,outputs.tf}`; update `terraform/environments/staging/aws/main.tf` to call it.
- **Services involved:** EKS, IAM (OIDC provider), VPC (from M7).
- **Expected output:** A running EKS cluster in the private subnets from M7, one system node group, cluster OIDC issuer URL as a module output.
- **Verify:** `aws eks update-kubeconfig` + `kubectl get nodes` shows Ready nodes across multiple AZs; `kubectl get pods -n kube-system` all Running.

### Milestone 10 — GCP GKE Cluster — Staging
**Depends on:** M8. **Parallel with:** M9.

- **Goal:** GCP analog of M9 — Standard mode (not Autopilot, per the architecture's explicit reasoning), Workload Identity enabled at the cluster level, node pools ready.
- **Files to create:** `terraform/modules/gcp/gke-cluster/{main.tf,variables.tf,outputs.tf}`; update `terraform/environments/staging/gcp/main.tf`.
- **Services involved:** GKE, IAM (Workload Identity pool), VPC (from M8).
- **Expected output:** A running VPC-native GKE Standard cluster with Workload Identity Federation enabled and one node pool.
- **Verify:** `gcloud container clusters get-credentials` + `kubectl get nodes` shows Ready nodes across zones; `gcloud container clusters describe` confirms `workloadIdentityConfig` is set.

---

## Track F — Application Deployment (convergence point)

### Milestone 11 — Shared Kubernetes App Module
**Depends on:** M6 (needs a real pushed image), M9, M10.

- **Goal:** Prove the "single reusable codebase" claim concretely — the exact same Helm chart, applied via the exact same Terraform module, deployed to both clusters.
- **Files to create:** `terraform/modules/kubernetes/app-deployment/{main.tf,variables.tf,outputs.tf}`, `terraform/modules/kubernetes/namespaces-rbac/...`, `helm/pulse-api/templates/{deployment.yaml,service.yaml,hpa.yaml,pdb.yaml,serviceaccount.yaml}`, `helm/pulse-api/values-staging-aws.yaml`, `helm/pulse-api/values-staging-gcp.yaml`.
- **Services involved:** EKS, GKE (no new cloud services — this module only talks to the Kubernetes API).
- **Expected output:** `pulse-api` running with 2 replicas on both clusters, HPA and PDB objects present, resource requests/limits and liveness/readiness/startup probes wired per the architecture's Kubernetes Design section.
- **Verify:** `kubectl get deploy,hpa,pdb -n app` shows healthy objects on **both** clusters using the **same** `terraform/modules/kubernetes/app-deployment` source; `kubectl exec` a pod and hit `/readyz` internally; kill a pod manually and confirm the ReplicaSet replaces it within seconds on both clusters.

---

## Track G — Ingress & Load Balancing (parallel AWS/GCP)

### Milestone 12 — AWS Ingress & ALB
**Depends on:** M9, M11. **Parallel with:** M13.

- **Goal:** Public HTTPS entry point into the AWS cluster, routed straight to pod IPs (IP-target mode), per the architecture's Networking/Request Flow sections.
- **Files to create:** `terraform/modules/aws/alb-controller/{main.tf,variables.tf,outputs.tf}` (installs AWS Load Balancer Controller via `helm_release` + its IRSA role), `helm/pulse-api/templates/ingress.yaml` (parameterized by cloud).
- **Services involved:** ALB, ACM, IAM (IRSA for the controller itself), EKS.
- **Expected output:** An ALB in the public subnets, target group in IP mode pointing at pod IPs, ACM cert attached, DNS name resolvable.
- **Verify:** `curl https://<alb-dns-name>/healthz` returns `200` over TLS; AWS console target group shows healthy IP targets matching current pod IPs exactly (confirms IP-mode, not instance-mode).

### Milestone 13 — GCP Ingress & Cloud Load Balancer
**Depends on:** M10, M11. **Parallel with:** M12.

- **Goal:** GCP analog of M12 — global external HTTPS LB via GKE's native Ingress, container-native NEGs.
- **Files to create:** `helm/pulse-api/templates/ingress.yaml` (shared file, GCP-specific values in `values-staging-gcp.yaml` — no separate controller install needed since GKE's ingress controller is built in, unlike AWS).
- **Services involved:** Cloud Load Balancer, Google-managed certs, GKE.
- **Expected output:** A global external HTTPS LB with a container-native NEG backend, anycast IP.
- **Verify:** `curl https://<gclb-ip>/healthz` returns `200` over TLS; `gcloud compute network-endpoint-groups list` shows a NEG whose endpoints match current pod IPs.

---

## Track H — Identity & Secrets (parallel AWS/GCP)

### Milestone 14 — AWS IAM Least Privilege (IRSA)
**Depends on:** M9. **Parallel with:** M15.

- **Goal:** Pods get scoped IAM roles via short-lived tokens — no node-wide credentials, no static keys.
- **Files to create:** `terraform/modules/aws/iam-irsa/{main.tf,variables.tf,outputs.tf}`.
- **Services involved:** IAM, EKS (OIDC provider from M9).
- **Expected output:** A reusable module producing one IAM role + trust policy per (namespace, service account) pair requested.
- **Verify:** From inside a pod annotated with the role, `aws sts get-caller-identity` returns the scoped role's ARN, not the node role; attempting an action outside the role's policy (e.g., listing an unrelated S3 bucket) is denied.

### Milestone 15 — GCP IAM Least Privilege (Workload Identity)
**Depends on:** M10. **Parallel with:** M14.

- **Goal:** GCP analog of M14.
- **Files to create:** `terraform/modules/gcp/iam-workload-identity/{main.tf,variables.tf,outputs.tf}`.
- **Services involved:** IAM, GKE (Workload Identity pool from M10).
- **Expected output:** A reusable module binding a Kubernetes service account to a scoped GCP service account.
- **Verify:** From inside a bound pod, the metadata-server-backed credential returns the GCP service account's identity, not the node's default service account; an out-of-scope API call is denied with a clear IAM error.

### Milestone 16 — AWS Secrets (Secrets Manager + CSI Driver)
**Depends on:** M14.

- **Goal:** Sensitive config reaches pods as mounted volumes via IRSA-scoped access, never as plain Kubernetes Secrets or env vars.
- **Files to create:** `terraform/modules/aws/secrets/{main.tf,variables.tf,outputs.tf}`, `helm/pulse-api/templates/secretproviderclass.yaml`.
- **Services involved:** Secrets Manager, KMS, IAM (IRSA from M14).
- **Expected output:** One test secret in Secrets Manager, mounted into the pod's filesystem via the Secrets Store CSI Driver.
- **Verify:** `kubectl exec` into the pod and `cat` the mounted secret path — value matches Secrets Manager; a pod using a *different* service account cannot mount the same SecretProviderClass (access denied).

### Milestone 17 — GCP Secrets (Secret Manager + CSI Driver)
**Depends on:** M15.

- **Goal:** GCP analog of M16.
- **Files to create:** `terraform/modules/gcp/secrets/{main.tf,variables.tf,outputs.tf}`, `helm/pulse-api/templates/secretproviderclass-gcp.yaml`.
- **Services involved:** Secret Manager, Cloud KMS, IAM (Workload Identity from M15).
- **Expected output:** Same as M16, GCP Secret Manager side.
- **Verify:** Same pattern as M16 — mount succeeds for the bound service account, fails for any other.

---

## Track I — Continuous Deployment

### Milestone 18 — CD Automation (GitHub Actions OIDC Deploy, Smoke Test, Rollback) — Staging
**Depends on:** M6, M12, M13, M14, M15.

- **Goal:** Close the loop from "merge to `main`" to "running on both staging clusters," with an automatic rollback if the post-deploy smoke test fails. This is also where the CI-facing OIDC trust (distinct from M14/M15's pod-facing IRSA/WI) gets built.
- **Files to create:** `.github/workflows/cd-app-staging.yml`, `.github/workflows/terraform-plan.yml`, `.github/workflows/terraform-apply.yml`, `terraform/modules/aws/github-oidc/...`, `terraform/modules/gcp/github-oidc/...`, `scripts/rollback.sh`, expanded `scripts/smoke-test.sh`.
- **Services involved:** IAM/OIDC (AWS + GCP, CI-facing), EKS, GKE.
- **Expected output:** Merging to `main` triggers: build/push (M6) → `helm upgrade` against both clusters using just-in-time OIDC credentials → smoke test → success, or automatic `helm rollback` on failure.
- **Verify:** Merge a good change — confirm both clusters run the new Git-SHA tag within the pipeline run. Merge a change that deliberately fails its smoke test — confirm the pipeline auto-rolls-back and both clusters end up back on the prior tag. Confirm no static cloud credential exists anywhere in repo secrets.

---

## Track J — Observability

### Milestone 19 — Unified Observability
**Depends on:** M11 (app running), M9, M10.

- **Goal:** The OTel-based "unified monitoring" story — identical app instrumentation, cloud-specific export destination, alerts routed to one shared channel.
- **Files to create:** `terraform/modules/aws/observability/{main.tf,variables.tf,outputs.tf}`, `terraform/modules/gcp/observability/...`, `terraform/modules/kubernetes/otel-collector/...`, `docs/runbooks/*.md`.
- **Services involved:** CloudWatch (Logs, Metrics, Alarms), SNS, Cloud Monitoring, Cloud Logging, GKE/EKS.
- **Expected output:** OTel Collector DaemonSet on both clusters; CloudWatch dashboard + Cloud Monitoring dashboard both showing live request-rate/latency/error-rate; one alarm/policy pair (e.g., error-rate) on each cloud, both notifying the same Slack channel.
- **Verify:** Generate synthetic load and confirm both dashboards reflect it within the expected scrape interval; deliberately trigger the error-rate alarm (e.g., return 500s from a test route) and confirm the Slack message arrives from **both** clouds' alerting systems within their configured evaluation window.

---

## Track K — DNS & Cross-Cloud Failover

### Milestone 20 — DNS & Cross-Cloud Failover
**Depends on:** M12, M13.

- **Goal:** The actual payoff of running two clouds — Route 53 health-checks both endpoints and routes (or fails over) between them.
- **Files to create:** `terraform/modules/aws/dns-acm/...` (already scoped in the architecture's module tree — now wired to accept the GCP load balancer's IP as an input), plus the cross-cloud variable wiring in `terraform/environments/staging/{aws,gcp}/main.tf` outputs.
- **Services involved:** Route 53, ACM, ALB (M12), Cloud Load Balancer (M13).
- **Expected output:** One hosted zone, two health checks, a weighted (or failover) routing policy across both endpoints.
- **Verify:** `dig` the domain repeatedly and confirm both IPs/hostnames are returned per the configured weight (or only the primary, if failover mode). Deliberately fail one cloud's health check (e.g., scale its deployment to 0) and measure actual wall-clock time until `dig` stops returning that endpoint — record the real number against the RTO estimate in the architecture doc rather than assuming it.

---

## Track L — Production

### Milestone 21 — Production Environment (AWS + GCP)
**Depends on:** M9 through M20, all proven in staging.

- **Goal:** Promote the proven staging pattern to HA-sized production, gated by manual approval, on both clouds.
- **Files to create:** `terraform/environments/prod/aws/*`, `terraform/environments/prod/gcp/*` (mirroring staging's file names with prod sizing/variables), `helm/pulse-api/values-prod-aws.yaml`, `helm/pulse-api/values-prod-gcp.yaml`, `.github/workflows/cd-app-prod.yml`.
- **Services involved:** All services from M7–M20, at production scale (multi-AZ NAT, wider HPA bounds, tighter PDBs).
- **Expected output:** Both production clusters live, reachable through the same Route 53 zone, deploys gated by a required GitHub Environment reviewer.
- **Verify:** A production deploy requires and waits for manual approval; post-approval, the same smoke test from M18 runs against prod; `kubectl get pdb,hpa -n app` in prod shows the stricter production bounds, distinct from staging's.

---

## Track M — Validation & Hardening

### Milestone 22 — Load Testing & Capacity Validation
**Depends on:** M21 (primary target), can run informally against M18's staging environment earlier.

- **Goal:** Replace the architecture doc's *design* claim about handling 100K users with actual measured evidence.
- **Files to create:** `scripts/load-test/*.js` (k6), `docs/runbooks/load-test-results.md`.
- **Services involved:** EKS, GKE, CloudWatch, Cloud Monitoring (to observe the test).
- **Expected output:** A k6 run against production-sized staging or prod hitting the target RPS profile from the architecture's Scalability section.
- **Verify:** p95/p99 latency and error rate stay within the SLO stated in Section 9 of the architecture doc throughout the test; HPA and cluster/node autoscaler visibly scale out and back down without manual intervention, observed directly in the dashboards from M19.

### Milestone 23 — Security & Cost Review Pass
**Depends on:** everything above.

- **Goal:** Close out the technical debt items the architecture doc flagged explicitly rather than letting them linger — policy-as-code enforcement, IAM verification, drift detection, actual-vs-claimed cost.
- **Files to create:** `terraform/policy/*.rego` (or checkov custom rules), `.github/workflows/scheduled-drift-check.yml`, `docs/adr/000X-*.md` (backfilled decisions), a short cost-reconciliation note in `docs/runbooks/`.
- **Services involved:** All (this is an audit pass, not a new service).
- **Expected output:** tfsec/checkov/cloudsplaining running in CI with zero high/critical findings; a nightly drift-check workflow; one month of actual billing data compared against Section 14's cost claims.
- **Verify:** CI fails a deliberately-introduced overly-broad IAM policy (proves the policy-as-code gate actually works, not just exists); the drift-check workflow posts to Slack when a manual console change is introduced as a test; actual monthly spend falls inside the ranges the architecture document claims.

---

## Quick-Reference Dependency Table

| # | Milestone | Track | Depends on |
|---|---|---|---|
| 0 | Repo scaffolding | A | — |
| 1 | Containerize app | A | 0 |
| 2 | TF bootstrap (AWS) | B | 0 |
| 3 | TF bootstrap (GCP) | B | 0 |
| 4 | ECR | B | 2 |
| 5 | Artifact Registry | B | 3 |
| 6 | CI build/scan/push | C | 1, 4, 5 |
| 7 | AWS networking | D | 2 |
| 8 | GCP networking | D | 3 |
| 9 | EKS cluster | E | 7 |
| 10 | GKE cluster | E | 8 |
| 11 | Shared K8s app module | F | 6, 9, 10 |
| 12 | AWS ingress/ALB | G | 9, 11 |
| 13 | GCP ingress/GCLB | G | 10, 11 |
| 14 | AWS IRSA | H | 9 |
| 15 | GCP Workload Identity | H | 10 |
| 16 | AWS Secrets Manager | H | 14 |
| 17 | GCP Secret Manager | H | 15 |
| 18 | CD automation (staging) | I | 6, 12, 13, 14, 15 |
| 19 | Unified observability | J | 9, 10, 11 |
| 20 | DNS & failover | K | 12, 13 |
| 21 | Production | L | 9–20 |
| 22 | Load testing | M | 21 |
| 23 | Security & cost review | M | all |

Everything in the same track (B, D, E, G, H) can be built in either order or by two people simultaneously — that's the deliberate minimization of cross-milestone dependency the split was designed for.
