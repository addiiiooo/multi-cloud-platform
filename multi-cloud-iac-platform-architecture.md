
# Multi-Cloud Infrastructure-as-Code Deployment Platform

### A production-grade portfolio architecture: one Terraform codebase, one microservice, two clouds

---

## 1. Executive Summary

This platform deploys a single stateless microservice — `pulse-api`, a small Go REST service exposing `/healthz`, `/readyz`, a couple of JSON CRUD-style routes, and a `/metrics` endpoint — to both AWS (via EKS) and Google Cloud (via GKE) from one Terraform repository. It is built to look and operate like a real small-company production system, not a tutorial: modular IaC, three environments, remote state, GitHub Actions CI/CD with OIDC auth, Kubernetes-native autoscaling and load balancing, unified observability, and least-privilege IAM on both clouds.

The project is deliberately scoped. It does not include a database, a service mesh, or a dozen microservices, because none of those are needed to demonstrate the four target competencies: multi-cloud IaC reuse, Kubernetes-based scaling/load balancing, cross-cloud observability, and least-privilege identity. Every additional service considered (ECS, Cloud Run, a service mesh, a GitOps controller) was evaluated and either included with a stated reason or explicitly excluded — that exclusion reasoning is documented in Section 3 and Section 15 rather than swept under the rug, because a hiring manager who has actually run infrastructure will ask "why didn't you use X" and "why do you need both clouds at all" before they ask anything else.

**Why two clouds, honestly.** The defensible engineering reason to run the same workload on AWS and GCP simultaneously is not resume padding — it's the one thing a single cloud structurally cannot give you: provider-level failure isolation and vendor-negotiation leverage. Route 53 health-checks both regional endpoints and can run in either an active-active (weighted) or active-passive (failover) configuration, so a full AWS regional/service outage does not take the product down. That is the *real* value being demonstrated, and it's called out explicitly in Sections 4 and 12 rather than left implicit.

**What "single reusable Terraform codebase" actually means here** (important nuance, addressed again in Section 10 and stress-tested in Section 16): Terraform resources are provider-specific — there is no HCL block that provisions both an EKS cluster and a GKE cluster at once, and any claim otherwise doesn't survive a technical interview. What *is* genuinely shared and reused: (1) the Kubernetes/Helm deployment layer, which is 100% identical HCL/Helm templates run against either cluster's kubeconfig; (2) a parallel variable/output contract between the `modules/aws/*` and `modules/gcp/*` trees, so both expose the same shape (`vpc_id`, `cluster_endpoint`, `private_subnet_ids`, etc.) and consuming code never branches on provider; (3) identical root-module structure, environment layout, CI pipeline shape, tagging conventions, and policy-as-code rules across both clouds. That's the realistic, interview-defensible version of "reusable."

**Resume-bullet mapping**, briefly: containerized deploy to both clouds from one codebase → Sections 2, 10; K8s autoscaling/load balancing → Section 8; unified CloudWatch + Cloud Monitoring observability → Section 9; least-privilege IAM/secrets across both clouds → Section 7. Each is backed by a specific, named mechanism (IRSA, Workload Identity, HPA, OTel), not a service name dropped without implementation detail.

**Constraints this design honors:** a real dev environment under $30/month (Section 14 shows the math, including why a persistent EKS cluster alone would blow that budget by more than 2x), a small team that can actually operate this without a dedicated platform org, and a production topology that scales to 100K+ users without a re-architecture.

---

## 2. High-Level Architecture

```
                           +------------------------------+
                           |            Client            |
                           +------------------------------+
                                                 |
                                           v  HTTPS (443)
                           +------------------------------+
                           |        Route 53 (DNS)        |
                           |   health-checked routing:    |
                           |     weighted / failover      |
                           +------------------------------+
                                           |
                      +--------------------+--------------------+
                      |                                         |
                      v                                         v
    +----------------------------------+      +----------------------------------+
    |               AWS                |      |               GCP                |
    +----------------------------------+      +----------------------------------+
    | ACM cert + AWS WAF (optional)    |      | Google cert + Cloud Armor        |
    |   |                              |      |   |                              |
    | Application Load Balancer        |      | Global External HTTPS LB         |
    | (public subnets, 3 AZs)          |      | (anycast IP, multi-zone)         |
    |   |                              |      |   |                              |
    | EKS Cluster (private subnets,    |      | GKE Cluster (private nodes,      |
    | 3 AZs)                           |      | 3 zones)                         |
    |                                  |      |                                  |
    | ns: app                          |      | ns: app                          |
    | [pod] [pod] [pod]  <-- HPA       |      | [pod] [pod] [pod]  <-- HPA       |
    |                                  |      |                                  |
    | node groups: on-demand + spot    |      | node pools: on-demand +          |
    | (managed by Karpenter)           |      | preemptible (autoscaled)         |
    |   |         |                    |      |   |         |                    |
    | ECR      Secrets Manager         |      | Artifact Reg.  Secret Mgr        |
    |                                  |      |                                  |
    | IAM (IRSA)                       |      | IAM (Workload Identity)          |
    | CloudWatch: Logs/Metrics/Alarms  |      | Cloud Logging / Monitoring       |
    | SNS -> Slack / email             |      | Alerting -> Slack / email        |
    +----------------------------------+      +----------------------------------+
                      |                                         |
                      +--------------------+--------------------+
                                           |
                                           v
                         +----------------------------------+
                         |          GitHub Actions          |
                         +----------------------------------+
                         | build - test - scan - push image |
                         | plan - apply - deploy - verify   |
                         | rollback on failed smoke test    |
                         | (OIDC federation - no static     |
                         |  cloud credentials in CI)        |
                         +----------------------------------+
                                           |
                                           v
                         +---------------------------------+
                         |      Remote Terraform State     |
                         +---------------------------------+
                         | S3 + DynamoDB  (AWS-side state) |
                         | GCS bucket     (GCP-side state) |
                         +---------------------------------+
```

Two things worth noting about this diagram before the service-by-service breakdown: the two cloud stacks are structurally parallel but not identical (GKE uses NEGs instead of IP-mode target groups, preemptible instead of spot terminology, Secret Manager instead of Secrets Manager) — that parallelism-without-pretending-to-be-identical is intentional. And Terraform state is deliberately *not* centralized in one cloud: AWS state lives in S3, GCP state lives in GCS, so a full outage of one provider never blocks the ability to operate or roll back the other.

---

## 3. Cloud Services

### AWS Services

**VPC.** Purpose: an isolated network boundary with public subnets (ALB, NAT) and private subnets (EKS nodes) spread across three Availability Zones. Why selected: it's the only way to get real network isolation, security-group-level segmentation, and AZ-level fault tolerance — the default VPC is explicitly not production-appropriate (shared, less controllable subnet/routing layout). Alternative: default VPC, or a flatter single-subnet design. Tradeoff: a custom multi-AZ VPC costs more to design and (via NAT gateways) to run, but it's the baseline every other control (IRSA, VPC endpoints, security groups, NetworkPolicy) depends on — there's no version of "production-grade" that skips it.

**EKS.** Purpose: managed Kubernetes control plane (API server, etcd, upgrades, HA) so the team runs workloads, not control-plane operations. Why selected: the resume goal is *Kubernetes* skill, specifically portable Kubernetes skill that also applies on GCP — that requirement alone rules out anything AWS-proprietary. Alternatives: self-managed Kubernetes on EC2 (kops/kubeadm) is materially cheaper (no control-plane fee) but pushes etcd backup, API-server HA, and version upgrades onto the team, which is a bad trade for a small team; **ECS is deliberately excluded** — it's cheaper (no control-plane fee) and simpler operationally, but it's AWS-proprietary, it would fragment the "Kubernetes-based autoscaling across both clouds" story into "Kubernetes on one cloud, something else on the other," and it teaches skills that don't transfer to GCP, Azure, or on-prem. Tradeoff: EKS costs $0.10/hour (~$73/month) per cluster while a Kubernetes version is in standard support, jumping to $0.60/hour (~$438/month) once that version ages into extended support — which is precisely why Section 14 does *not* run a persistent EKS cluster in dev, and why staying current on Kubernetes minor versions is a real cost decision, not just hygiene.

**ALB (via AWS Load Balancer Controller).** Purpose: Layer 7 load balancing, TLS termination, host/path routing in front of the cluster. Why selected: the AWS Load Balancer Controller provisions ALB target groups in **IP mode**, registering pod IPs directly (via the VPC CNI) instead of routing through a NodePort and an extra kube-proxy hop — lower latency, and the ALB sees real client source IPs. Alternative: a self-hosted ingress controller (e.g., NGINX Ingress) behind a Network Load Balancer — more portable and avoids ALB's per-rule/LCU pricing, but hands the team TLS-cert lifecycle and controller-upgrade ownership instead of AWS. Tradeoff: ALB runs roughly $16-20/month plus LCU (load balancer capacity unit) charges, and requires IAM-permissioning and installing the controller itself — a one-time setup cost for ongoing operational simplicity.

**IAM (with IRSA).** Purpose: identity and access control for both humans and workloads. Why selected: IAM Roles for Service Accounts lets individual pods assume narrowly-scoped IAM roles via short-lived STS tokens, rather than the entire node (and every pod on it) inheriting one broad node IAM role. Alternative: static IAM access keys stored as Kubernetes Secrets — functional, but long-lived, rotated manually or not at all, and a textbook credential-leak vector. Tradeoff: IRSA requires associating an OIDC provider with the cluster and writing one trust policy per service account, a small upfront cost for a large security payoff at effectively zero runtime cost.

**CloudWatch (Logs, Metrics, Alarms, Container Insights).** Purpose: the AWS-side half of unified observability — cluster and application logs, custom application metrics, and alarm-driven alerting. Why selected: native IAM integration, no extra infrastructure to run, and it's what the resume bullet explicitly names. Alternative: self-hosted Prometheus + Grafana + Loki — more portable across clouds and avoids per-metric/per-GB ingestion costs, but is itself infrastructure that needs patching, scaling, and on-call ownership, which is a poor trade for a small team. Tradeoff: CloudWatch cost scales with log volume and custom-metric cardinality — high-cardinality dimensions (e.g., putting a user ID on a metric) are a well-known way to accidentally 10x this bill, so Section 9's metric design deliberately avoids unbounded label values.

**ECR.** Purpose: private container registry for images the EKS side pulls from, with vulnerability scan-on-push. Why selected: tight IAM integration (pull permissions via the node/pod role, no separate registry credentials) and negligible cost. Alternative: Docker Hub — simpler for a single-cloud demo, but weaker private-image access control and rate-limited pulls. Tradeoff: essentially free at this scale (~$0.10/GB-month storage); the only real cost is remembering to set lifecycle policies so old tags don't accumulate indefinitely.

**Secrets Manager.** Purpose: storage and (optionally) automatic rotation of sensitive configuration — API keys, TLS material — mounted into pods via the Secrets Store CSI Driver rather than baked into images or plain Kubernetes Secrets. Why selected: native rotation hooks and fine-grained IAM-scoped access per secret. Alternative: SSM Parameter Store `SecureString` — materially cheaper (no per-secret fee, generous free tier) but no built-in rotation workflow; this design actually uses Parameter Store for non-rotating, lower-sensitivity config and reserves Secrets Manager for anything that benefits from rotation, rather than defaulting everything into the pricier service out of laziness. Tradeoff: Secrets Manager is $0.40 per secret per month plus $0.05 per 10,000 API calls — trivial at the handful of secrets this service needs, but a line item worth watching if it were to grow to hundreds of secrets or per-region replicas (each replica bills as its own secret).

**Route 53.** Purpose: the single authoritative DNS layer for the whole platform, including health-check-based routing between the AWS and GCP endpoints. Why selected: rather than run Route 53 as authoritative for the AWS side and Cloud DNS for the GCP side (split-horizon complexity, two systems to reason about), one DNS provider is authoritative for the whole apex domain and simply holds health-checked records pointing at *both* the ALB and the Google Cloud Load Balancer — DNS doesn't care which cloud a record's target lives in. Alternative: Cloud DNS as the single authority instead — a legitimate, roughly-equivalent choice; Route 53 was picked arbitrarily between two comparable options, not because Cloud DNS is worse. Tradeoff: Route 53 costs about $0.50/hosted-zone/month plus per-query charges (negligible at this scale) and per-health-check charges; the real tradeoff to be honest about is that DNS-based failover has a floor RTO set by TTL and resolver caching behavior — it is not instant, and Section 12 states a realistic number instead of implying zero-downtime failover.

### Google Cloud Services

**VPC.** Purpose and rationale mirror the AWS VPC exactly: isolated custom-mode network, regional subnets, private (no external IP) nodes. GKE uses a **VPC-native cluster** with secondary IP ranges for pods and services (alias IPs), which is the direct GCP analog of AWS's IP-target-mode routing — both clouds end up routing straight to pod IPs instead of through an extra proxy hop. Tradeoff: the VPC itself is free; cost lives in NAT and the load balancer, same as AWS.

**GKE.** Purpose: managed Kubernetes control plane on GCP. Why selected: same portability argument as EKS — this is the whole reason the project exists. Mode chosen: **Standard**, not Autopilot. Why: Autopilot abstracts away node pools, taints/tolerations, and scheduling entirely, which is operationally simpler but leaves nothing to design in Section 8 — since this project exists specifically to demonstrate node architecture and scheduling judgment, Standard mode (with real node pools, taints, and a cluster autoscaler) is the more honest choice for a *portfolio* project, even though Autopilot would genuinely be the lower-toil choice for a real small team. That tradeoff — "chose the more demonstrative option over the operationally lazier one, and can defend why" — is exactly the kind of judgment call worth stating out loud in an interview. Alternative: Cloud Run is **deliberately excluded** for the same reason ECS is excluded on the AWS side — it's a fully-managed, GCP-proprietary serverless container platform, not Kubernetes, and using it would mean the "unified Kubernetes-based autoscaling across both clouds" claim is only true on one side. Tradeoff: flat $0.10/hour cluster-management fee — but see Section 14: GCP waives this fee (as a $74.40/month account credit) for exactly one zonal Standard or Autopilot cluster per billing account, which AWS has no equivalent of, and that asymmetry directly shapes which cloud hosts the "always-on" dev cluster.

**Artifact Registry.** Purpose: GCP-native container registry for images the GKE side pulls, with integrated vulnerability scanning. Why selected: successor to the deprecated Container Registry, tight IAM/Workload Identity integration, supports regional replication. Alternative/tradeoff: same shape as ECR — Docker Hub is simpler but weaker on private access control; cost is negligible at this image volume.

**Cloud Monitoring & Cloud Logging.** Purpose: the GCP-side half of unified observability — metrics, dashboards, and log aggregation, integrated with GKE out of the box via Google Cloud's Operations suite. Why selected: zero additional infrastructure to run, and named explicitly in the target resume bullet. Alternative: the same self-hosted Prometheus/Grafana/Loki stack considered on the AWS side — more portable, more ops burden, rejected for the same small-team reason. Tradeoff: same cardinality/cost-blowup risk as CloudWatch; sampling and metric-label discipline matter here too.

**Secret Manager.** Purpose and role are the direct GCP analog of AWS Secrets Manager: encrypted secret storage, versioning, IAM-scoped access, integrated with Workload Identity so pods fetch secrets using their own bound identity rather than a mounted key file. Alternative: Kubernetes-native Secrets (unencrypted at rest unless envelope-encrypted with Cloud KMS, no native rotation) — clearly weaker. Tradeoff: roughly $0.06 per active secret version per month plus access-operation charges — cheap at this scale, same "watch it if this grows to hundreds of secrets" caveat as the AWS side.

**Cloud Load Balancer.** Purpose: GKE's Ingress resource provisions a Google Cloud external HTTP(S) Load Balancer — a global, anycast-IP, Layer 7 load balancer with optional Cloud CDN and Cloud Armor (WAF/DDoS) attachment. Why selected: the anycast global frontend is a genuine capability advantage over a regional ALB — a client is routed to the nearest healthy backend automatically at the network layer. Alternative: a regional Network Load Balancer (L4 only, no host/path routing) or a self-managed NGINX ingress controller (more portable, loses the global-anycast and Cloud Armor integration). Tradeoff: a forwarding rule plus data-processing charges, roughly $18-25/month at low volume — the cost of a genuinely global entry point.

**IAM (with Workload Identity).** Purpose: GCP's equivalent of IRSA — binds a Kubernetes service account to a GCP IAM service account (`roles/iam.workloadIdentityUser`), so pods obtain scoped GCP credentials automatically with no key file ever touching disk. Alternative: mounting a downloaded service-account-key JSON file into the pod — the exact anti-pattern Workload Identity was built to eliminate (long-lived, exportable, easy to leak). Tradeoff: one-time setup (enabling Workload Identity Federation on the cluster, annotating service accounts) for credentials that are short-lived and audit-logged by default.

---

## 4. Request Flow

1. **DNS resolution.** The client resolves the apex/API domain against Route 53. Depending on the configured routing policy, Route 53 returns either a weighted mix of the AWS ALB and GCP Load Balancer addresses (active-active) or the primary cloud's address, falling over to the secondary only if that cloud's health check is failing (active-passive). Each record is backed by a Route 53 health check hitting `/healthz` on the respective load balancer roughly every 10-30 seconds.
2. **Edge / TLS termination.** On the AWS path, the request hits the ALB in a public subnet; TLS is terminated using an ACM certificate, and (if enabled) AWS WAF evaluates the request against managed rule sets before it reaches a target group. On the GCP path, the request hits the global external HTTPS Load Balancer's nearest anycast point of presence; TLS terminates on a Google-managed certificate, and Cloud Armor evaluates it if enabled.
3. **Routing to pods.** The ALB (IP-target-mode target group) or the GCLB (container-native NEG) forwards the request directly to a pod IP inside the private subnet/node — bypassing the extra NodePort/kube-proxy hop that a "classic" instance-mode setup would incur on either cloud.
4. **Admission at the pod.** The receiving pod only accepts the connection if its readiness probe (`/readyz`) is currently passing; a pod that has fallen out of readiness (e.g., because a dependency check is failing) is already removed from the Service's endpoint list and never receives traffic in the first place.
5. **Network policy enforcement.** Before the request reaches the application container, the CNI-level NetworkPolicy is evaluated — only the load-balancer-facing path is allowed ingress to the `app` namespace; pod-to-pod traffic outside that is default-deny.
6. **Application processing.** `pulse-api` handles the request, emits a structured JSON log line and Prometheus-format metrics, and (if instrumented) a trace span via the OpenTelemetry SDK.
7. **Telemetry export.** An OpenTelemetry Collector (DaemonSet) running in the same cluster scrapes/receives that telemetry and exports it to the cluster's native backend — CloudWatch Logs/Metrics on EKS, Cloud Logging/Monitoring on GKE — so the instrumentation code in the app is identical on both clouds even though the destination differs.
8. **Response path.** The response returns back through the same load balancer, and the client's connection is closed or kept alive per HTTP keep-alive settings.
9. **Failure path.** If the target cloud's ALB/GCLB health check fails outright (region/service outage), Route 53 stops returning that endpoint's address on subsequent DNS resolutions, and new client lookups resolve to the healthy cloud — see Section 12 for the realistic timing of this failover.

---

## 5. CI/CD Flow

**Developer workflow.** A developer branches from `main`, makes an application or Terraform change, opens a PR. Branch protection requires passing status checks and at least one review before merge; direct pushes to `main` are blocked.

**Pull request pipeline (`ci-app.yml` / `terraform-plan.yml`).** On every PR: the application is linted and unit-tested; the Docker image is built (but not pushed); Trivy scans the built image for known CVEs; if the PR touches `terraform/`, `terraform fmt -check` and `terraform validate` run for every affected root module, `tfsec`/`checkov` run policy checks, and `terraform plan` output is posted as a PR comment for human review before anyone applies anything. A failing scan or a plan that would destroy a production resource blocks merge via required status checks.

**Image build and dual-push.** On merge to `main`, the image is built once, tagged immutably with the Git SHA (never `:latest` in any environment manifest), and pushed to **both** ECR and Artifact Registry in the same job — one build artifact, two destinations, which is the concrete mechanism behind "build once, deploy to both clouds."

**Infrastructure deployment.** `terraform-apply.yml` runs only on changes under `terraform/`, applies to `dev` automatically, and requires a manual approval gate (a GitHub Environment with required reviewers) before applying to `staging` or `prod`. Each cloud's root module applies independently, so a GCP-side Terraform error never blocks or half-applies the AWS side.

**Application deployment.** `cd-app-*.yml` runs a `helm upgrade` (or `kubectl apply` on the rendered manifests) against each cluster's kubeconfig, obtained just-in-time via short-lived OIDC-federated credentials — the runner never holds a static cloud credential. Deployment proceeds environment by environment: dev deploys automatically on merge; staging deploys automatically once a post-deploy smoke test against dev passes; production requires manual approval and deploys to both clouds in the same job so they never drift out of sync on which image tag is live.

**Post-deploy verification and rollback.** Every deployment stage runs a smoke test (a scripted set of requests against `/healthz`, `/readyz`, and one real endpoint) immediately after rollout. If the smoke test fails, or if the Kubernetes rollout itself reports `Deployment` conditions not reaching `Available` within a timeout, the pipeline automatically runs a rollback — `helm rollback` (or `kubectl rollout undo`) to the previous release, which is deterministic because every release is keyed to an immutable Git-SHA image tag. Because Terraform is declarative, there is no separate "infrastructure rollback" command — an infra rollback is simply reverting the offending commit and re-applying, restoring the last known-good `.tf` state.

---

## 6. Networking

**VPC/subnet layout (AWS, repeated per environment with a distinct CIDR block — e.g., dev `10.0.0.0/16`, staging `10.1.0.0/16`, prod `10.2.0.0/16`):** three public subnets (one per AZ, `/24`) hosting the ALB and NAT gateway(s); three private subnets (one per AZ, `/24`) hosting EKS worker nodes, with pod IPs allocated from the VPC CNI's secondary ENI ranges. Route tables send private-subnet egress through NAT, public-subnet egress directly to an Internet Gateway.

**VPC/subnet layout (GCP):** a custom-mode VPC with one regional subnet per environment, a primary IP range for nodes, and two secondary ranges (pods, services) enabling a VPC-native GKE cluster. Cloud NAT provides outbound-only internet access for private nodes; Private Google Access lets nodes reach Google APIs (Artifact Registry, Secret Manager, Cloud Logging) without traversing the public internet at all.

**Security groups (AWS).** The ALB security group allows inbound 443/80 from `0.0.0.0/0` only. The node/pod security group allows inbound only from the ALB security group (by reference, not CIDR) plus intra-cluster traffic; all other inbound is denied by default. Egress from private subnets is restricted to NAT/VPC-endpoint traffic.

**Firewall rules (GCP).** Ingress is allowed only from the GCLB's documented health-check IP ranges and from Cloud Armor-fronted traffic; all other ingress to node/pod IP ranges is denied by default. Egress routes through Cloud NAT.

**VPC/Private endpoints.** On AWS, Gateway/Interface VPC Endpoints for S3 (Terraform state), ECR, Secrets Manager, and CloudWatch keep that traffic on the AWS backbone instead of routing through NAT — this is both a security improvement and a real cost optimization, since NAT data-processing charges apply per GB and add up quickly on chatty telemetry traffic. On GCP, Private Google Access serves the equivalent purpose for Artifact Registry, Secret Manager, and Cloud Logging/Monitoring.

**Kubernetes NetworkPolicy (both clusters).** Default-deny ingress and egress at the namespace level, with explicit allow rules: only the ingress path (ALB/GCLB → pod) is permitted inbound to the `app` namespace; the application's egress is limited to DNS, the telemetry collector, and the specific Secrets Manager/Secret Manager VPC endpoint — not "allow all egress," which is the common default that quietly defeats network segmentation. GKE enforces this via its Dataplane V2 (Cilium-based) CNI; EKS enforces it via the VPC CNI's native NetworkPolicy support (or Calico, if finer-grained policy is needed later).

**Load balancers and Ingress.** Covered in Section 3/4 — ALB via the AWS Load Balancer Controller (IP-target mode), GCLB via GKE's native Ingress controller (container-native NEGs). Both are configured through the same conceptual Kubernetes `Ingress` object, with cloud-specific annotations supplied as Terraform variables — this is one of the concrete places where "one codebase, two clouds" is literally true rather than aspirational.

**DNS and traffic routing.** Route 53 is authoritative for the apex domain; two record sets (or one weighted pair) point at the ALB's DNS name and the GCLB's anycast IP, each backed by an independent health check. The routing policy — weighted 50/50 for an active-active demo, or primary/secondary for active-passive — is a Terraform variable, not a hardcoded choice, so the same codebase can demonstrate either failure-handling model.

---

## 7. Security Architecture

**IAM and least privilege.** Every workload identity is scoped to exactly what it needs and nothing else: IRSA roles on AWS and Workload Identity bindings on GCP are defined per-service-account, per-namespace, with resource-level conditions (e.g., an IAM policy naming the specific Secrets Manager ARN prefix, not `secretsmanager:*` on `*`). CI/CD identities are split further — the "plan" role can read but not write infrastructure; only the "apply" role (used solely in the post-approval job) can create/modify/destroy resources, and even that role is scoped by resource tag conditions so a compromised `prod` apply credential can't touch `dev` resources tagged to a different environment.

**OIDC everywhere.** GitHub Actions authenticates to AWS via an IAM OIDC identity provider trusting `token.actions.githubusercontent.com`, with the role's trust policy conditioned on the repository and branch/environment claim in the token — so only workflows running on `main` (or a specific `environment:`) in this exact repo can assume the role. GCP authentication works the same way via Workload Identity Federation (a pool + provider mapped to the same GitHub OIDC issuer). The result: **zero long-lived cloud credentials exist anywhere in GitHub Secrets.** Every credential used by CI is minted per-run and expires in under an hour.

**Encryption.** At rest: EBS/Persistent Disk volumes encrypted with KMS/Cloud KMS customer-managed keys; S3 and GCS buckets (including Terraform state) default-encrypted with versioning enabled; Secrets Manager/Secret Manager apply envelope encryption automatically. In transit: TLS terminates at both load balancers using ACM/Google-managed certificates, renewed automatically; mTLS between services is **not** implemented — with a single microservice there's no service-to-service traffic to protect, and introducing a mesh purely to enable mTLS nobody needs yet is exactly the kind of unjustified complexity this design avoids (flagged again in Section 15 as a "when it would become worth it" item).

**Container security.** Images build from a minimal/distroless base, run as a non-root UID, use a read-only root filesystem, and drop all Linux capabilities not explicitly required. Both clusters enforce the Kubernetes **Pod Security Admission** `restricted` profile at the namespace level (the modern replacement for the deprecated PodSecurityPolicy), so a manifest that tries to run privileged or as root is rejected at admission time, not caught later in an audit.

**Supply chain security.** Trivy scans every image in CI before it's pushed anywhere; ECR and Artifact Registry additionally scan on push as a second, registry-native check. A Syft-generated SBOM is attached to each build. GitHub Actions workflow files pin third-party actions to a commit SHA, never a mutable tag or branch. Dependabot keeps both application and Action dependencies current. Image signing via cosign/Sigstore keyless signing is included as a near-term hardening step (Section 17) rather than claimed as already-enforced-at-admission — being explicit about "signed" vs. "signed *and verified by an admission controller*" is exactly the distinction a skeptical reviewer will probe (see Section 16).

**Network isolation.** Covered in depth in Section 6: private subnets for all compute, security-group/firewall rules scoped to the load balancer as the only public entry point, and default-deny NetworkPolicy inside the cluster.

**Authentication and authorization, three distinct layers.** (1) *Humans → cloud/cluster:* engineers authenticate via federated SSO (IAM Identity Center on AWS, Google Workspace/Cloud Identity on GCP) — nobody has a standing IAM user with a password, and `kubectl` access maps IAM/Google identity to Kubernetes RBAC roles, so there is no shared cluster-admin kubeconfig floating around. (2) *CI/CD → cloud:* the OIDC federation described above. (3) *End user → application:* explicitly out of scope for this infrastructure-focused project — `pulse-api` doesn't implement end-user auth, and the design simply notes where it would plug in (an API Gateway/Cognito or Identity Platform layer in front of the existing load balancers) rather than bolting on auth logic that would distract from the infrastructure story this project is telling.

---

## 8. Kubernetes Design

**Environment isolation strategy.** Each environment (dev/staging/prod) gets its **own cluster** per cloud rather than shared clusters split by namespace. This costs more than namespace-based multi-tenancy but avoids noisy-neighbor resource contention and, more importantly, blast radius: a misconfigured NetworkPolicy or an accidental `kubectl delete` in a lower environment cannot reach production because there is no network path or credential that spans both.

**Namespaces (within a given cluster).** `app` (the microservice and its ServiceAccount/RBAC), `observability` (the OpenTelemetry Collector DaemonSet and any self-hosted exporters), plus the provider-managed `kube-system` and the load-balancer-controller's own namespace. Each application namespace carries a `ResourceQuota` and `LimitRange` so a single bad deploy can't consume the whole node pool.

**Deployment.** A standard Kubernetes `Deployment` for `pulse-api`, `strategy: RollingUpdate` with `maxSurge: 25%` / `maxUnavailable: 0` (genuinely zero-downtime rollouts), pod anti-affinity and `topologySpreadConstraints` spreading replicas across AZs/zones and nodes so a single node or AZ loss never removes more than a fraction of capacity.

**Service and Ingress.** A `ClusterIP` Service backs the Deployment; it's exposed externally only through the cloud-specific Ingress object described in Sections 3/6. No headless Service is needed — the workload is stateless.

**Autoscaling.** A Horizontal Pod Autoscaler targets 60% average CPU utilization plus a custom requests-per-second metric (via the Prometheus Adapter/KEDA), with `minReplicas: 2` / `maxReplicas: 10` in dev/staging and `minReplicas: 3` / `maxReplicas: 20` in production — the floor of 2-3 exists purely for availability, independent of load. At the node layer: **Karpenter** on AWS (not the older Cluster Autoscaler) provisions right-sized instances just-in-time, including Spot capacity for this stateless, horizontally-redundant workload, with an SQS+EventBridge pipeline handling Spot interruption/rebalance notices gracefully; **GKE Cluster Autoscaler** (with node pools sized similarly, using preemptible VMs for the same reason) on the GCP side — Karpenter itself is AWS-specific, so this is one of the places the two clouds genuinely use different tooling to reach the same outcome, and pretending otherwise would be dishonest.

**Resource requests/limits.** `requests: cpu 100m, memory 128Mi`; `limits: memory 256Mi` — but **no hard CPU limit** is set. This is a deliberate, slightly non-obvious choice: a CPU limit is enforced via the kernel's CFS quota mechanism, which is well-documented to cause artificial throttling under bursty load even when a node has spare CPU capacity sitting idle. Memory is different — it's not compressible, so an unbounded pod really can starve its neighbors or trigger an OOM kill, which is why memory keeps a hard limit while CPU relies on requests (for scheduling fairness) plus the HPA (for horizontal response to sustained load) instead.

**Health probes.** `livenessProbe` on `/healthz` (process-level liveness only — catches deadlocks and hangs, restarts the container); `readinessProbe` on `/readyz` (checks that dependencies like the Secrets Manager/Secret Manager endpoint are reachable, and pulls the pod out of Service endpoints if not); `startupProbe` to give slow-starting containers grace before liveness checks begin, preventing a slow boot from being mistaken for a hang.

**Rolling updates and disruption budgets.** Rolling update parameters as above, plus a `PodDisruptionBudget` (`minAvailable: 50%` or a fixed floor, environment-dependent) so voluntary disruptions — node drains during upgrades, cluster autoscaler consolidation — can never take the service below a safe replica count.

**Node architecture.** Managed node groups (EKS) / node pools (GKE Standard), split into a small system pool (tainted, running only cluster add-ons) and an application pool (on-demand baseline + Spot/preemptible burst capacity). Where the application supports it, ARM64 (Graviton on AWS, Tau T2A/Axion-class on GCP) nodes are used for meaningfully better price-performance, built via multi-arch Docker images (`docker buildx`).

**Scheduling.** Taints/tolerations separate system workloads from application workloads and cordon off Spot/preemptible nodes so only interruption-tolerant pods land there; `PriorityClass` ensures the application's pods preempt any lower-priority batch workload before the application itself is evicted under resource pressure.

---

## 9. Observability

**Logging.** The application writes structured JSON to stdout/stderr (never to a file); a node-level agent (CloudWatch Container Insights on EKS, the Cloud Logging agent on GKE — both are Fluent Bit under the hood) ships those lines to CloudWatch Logs / Cloud Logging respectively, organized into log groups/buckets per cluster and namespace, with a retention policy that's short in dev (a few days) and longer in production (90 days), set explicitly rather than left at the provider default.

**Metrics.** `pulse-api` exposes Prometheus-format metrics on `/metrics`. An OpenTelemetry Collector DaemonSet scrapes them and exports to each cloud's native backend — CloudWatch via Embedded Metric Format (cost-efficient at this volume) on EKS, the OTLP-to-Cloud-Monitoring exporter on GKE. This is the concrete mechanism behind "unified monitoring": the instrumentation in the application code is identical on both clouds; only the Collector's export configuration differs, which is exactly the kind of difference that belongs in infrastructure config, not application code. Metric labels are deliberately kept low-cardinality (route, method, status class — never a raw user ID or request ID) specifically to avoid the cost blowup called out in Section 3.

**Tracing.** The OpenTelemetry SDK instruments the application; the same Collector exports spans to X-Ray on the AWS side and Cloud Trace on the GCP side. Sampling starts at or near 100% given the traffic volumes this project actually runs, with an explicit note that sampling must be tuned down well before Section 13's 100K-user scale point, or trace ingestion cost scales linearly with request volume.

**Dashboards.** Native CloudWatch Dashboards and Cloud Monitoring Dashboards give each cloud its own operational view. For a genuine single pane of glass across both, a small self-hosted Grafana instance (or the Grafana Cloud free tier) with the CloudWatch and Google Cloud Monitoring data-source plugins is the recommended optional addition — explicitly optional, because native per-cloud dashboards satisfy the resume bullet on their own, and Grafana is one more thing to operate.

**Alerting.** CloudWatch Alarms (error rate, p99 latency, pod restart count, node disk pressure) route through SNS; Cloud Monitoring Alerting Policies cover the identical thresholds on the GCP side. Both route notifications to the **same Slack channel** (via SNS→webhook and Cloud Monitoring's native webhook notification channel), so on-call isn't watching two separate alerting front doors — a deliberate consolidation called out again as a requirement, not a nice-to-have, in Section 16.

**Incident response.** Runbooks live in-repo (`docs/runbooks/`) — one per alert type, written before the alert is needed, not after the first incident. SLOs are defined explicitly (e.g., 99.9% monthly availability, p95 latency under 300ms) with the corresponding error budget (about 43 minutes/month at 99.9%) used to calibrate alert thresholds rather than picking numbers arbitrarily. A lightweight postmortem template is included; a full PagerDuty-style on-call rotation is deliberately not provisioned at this project's scale — Slack plus email/SMS notification channels are proportionate for a small-team portfolio system, and upgrading to a dedicated on-call tool is noted as a scale-triggered future step, not a day-one requirement.

---

## 10. Terraform Structure

```
terraform/
├── global/
│   └── bootstrap/                    # one-time, run manually, own local state:
│       ├── aws_backend.tf            #   S3 bucket (versioned, encrypted) + DynamoDB lock table
│       ├── gcp_backend.tf            #   GCS bucket (versioned, uniform access) for GCP state
│       └── README.md                 #   why this exists outside the normal apply pipeline
│
├── modules/
│   ├── aws/
│   │   ├── network/                  # VPC, public/private subnets, route tables, NAT, VPC endpoints
│   │   ├── eks-cluster/              # EKS control plane, OIDC provider, managed node groups, Karpenter
│   │   ├── ecr/                      # repositories, lifecycle policies, scan-on-push config
│   │   ├── iam-irsa/                 # reusable module: IRSA trust policy + policy attachment per SA
│   │   ├── secrets/                  # Secrets Manager secrets, KMS key, Parameter Store entries
│   │   ├── dns-acm/                  # Route 53 records/health checks, ACM certificate + validation
│   │   └── observability/            # CloudWatch log groups, dashboards, alarms, SNS topics
│   │
│   ├── gcp/
│   │   ├── network/                  # VPC, regional subnet w/ secondary ranges, Cloud NAT, firewall rules
│   │   ├── gke-cluster/               # GKE cluster (Standard), node pools, Workload Identity binding
│   │   ├── artifact-registry/        # repository, IAM bindings, cleanup policies
│   │   ├── iam-workload-identity/    # reusable module: KSA <-> GSA binding
│   │   ├── secrets/                  # Secret Manager secrets, Cloud KMS key
│   │   ├── dns-lb/                   # health checks feeding into the shared Route 53 module's inputs
│   │   └── observability/            # log sinks, dashboards, alerting policies, notification channels
│   │
│   └── kubernetes/                   # cloud-agnostic: targets whichever kubeconfig context is passed in
│       ├── app-deployment/           # Deployment, Service, HPA, PDB, ServiceAccount (Helm or k8s provider)
│       ├── ingress/                  # Ingress resource; cloud-specific annotations passed as variables
│       └── namespaces-rbac/          # namespace, ResourceQuota, LimitRange, RBAC role bindings
│
├── environments/
│   ├── dev/
│   │   ├── gcp/                      # only real cloud dev environment (see Section 14 for why)
│   │   └── README.md                 # documents the local kind/k3d workflow used for the AWS side in dev
│   ├── staging/
│   │   ├── aws/                      # calls modules/aws/* + modules/kubernetes/*; backend key: envs/staging/aws
│   │   └── gcp/                      # calls modules/gcp/* + modules/kubernetes/*; backend prefix: envs/staging/gcp
│   └── prod/
│       ├── aws/
│       └── gcp/
│
└── policy/                           # tfsec/checkov custom rules, run in CI against every plan
```

**Why each module exists.** `network`, `*-cluster`, and the registry modules exist as separate modules (rather than one monolithic root file) so each can be planned, tested, and reasoned about independently — a change to node-pool sizing shouldn't require re-planning VPC routing. `iam-irsa` / `iam-workload-identity` are extracted specifically because they're called repeatedly (once per workload identity needed) with only the role name and permission scope changing — exactly the DRY case a module exists for. `modules/kubernetes/*` is the one tree that is **literally** shared, unmodified, between both clouds, which is the concrete proof behind the "single reusable codebase" claim rather than just an assertion. `environments/*/aws` and `environments/*/gcp` are separate root modules (not Terraform workspaces) so that state files are physically separate per environment — a `terraform apply` run against `staging` cannot accidentally touch `prod` state because there is no shared workspace selector to mis-click. `policy/` exists so security and cost rules are enforced the same way in CI regardless of which environment or cloud is being planned.

**Why dev has no `environments/dev/aws` directory:** see Section 14 — a persistent EKS cluster alone costs more than the entire dev budget, so the AWS side of "dev" is a local kind/k3d cluster plus `terraform validate`/`plan` against the `aws/*` modules in CI (proving the code is correct without paying to run it continuously).

---

## 11. GitHub Repository Structure

```
repo/
├── .github/
│   ├── workflows/
│   │   ├── ci-app.yml                  # lint, unit test, build image, Trivy scan (PR)
│   │   ├── cd-app-dev.yml              # auto-deploy to dev on merge to main
│   │   ├── cd-app-staging.yml          # auto-deploy to staging after dev smoke test passes
│   │   ├── cd-app-prod.yml             # manual-approval deploy to prod (GitHub Environments)
│   │   ├── terraform-plan.yml          # plan + tfsec/checkov on PRs touching terraform/**
│   │   ├── terraform-apply.yml         # apply on merge, per-environment approval gates
│   │   └── scheduled-drift-check.yml   # nightly plan-only run -> Slack if drift is detected
│   ├── CODEOWNERS                      # infra changes require infra-owner review
│   └── pull_request_template.md        # checklist: tests, security scan, plan reviewed
│
├── app/
│   ├── src/                            # pulse-api source (Go)
│   ├── tests/                          # unit + smoke tests
│   ├── Dockerfile                      # multi-stage, distroless final layer, non-root
│   └── ...
│
├── terraform/                          # as detailed in Section 10
│
├── helm/
│   └── pulse-api/
│       ├── templates/                  # Deployment, Service, Ingress, HPA, PDB manifests
│       ├── values-dev.yaml
│       ├── values-staging.yaml
│       └── values-prod.yaml
│
├── scripts/
│   ├── bootstrap-local-dev.sh          # spins up kind/k3d cluster for AWS-side local dev
│   ├── smoke-test.sh                   # used by both CI and manual verification
│   └── rollback.sh                     # wraps helm rollback / rollout undo consistently
│
├── docs/
│   ├── architecture.md                 # this document, or a link to it
│   ├── runbooks/                       # one file per alert/incident type
│   └── adr/                            # Architecture Decision Records — why EKS vs ECS, etc.
│
├── .pre-commit-config.yaml             # terraform fmt, tflint, trivy, gofmt — caught before CI
├── SECURITY.md                         # how to report a vulnerability, supported versions
└── README.md
```

**Why this layout.** `app/`, `terraform/`, and `helm/` are separated at the top level so each has its own lint/test tooling and its own reviewers without cross-contaminating CI triggers (a Helm values change shouldn't re-run Go unit tests). `docs/adr/` specifically exists because a portfolio project's value in an interview is not just the final design but the *decision trail* — an ADR for "why EKS over ECS" or "why separate clusters per environment over namespaces" turns this repository into evidence of judgment, not just output. `scripts/rollback.sh` exists as a thin wrapper specifically so rollback is one documented, tested command — not something improvised for the first time during an actual incident.

---

## 12. Failure Scenarios

**Region or cloud-provider failure.** Route 53 health checks against the affected ALB/GCLB begin failing; Route 53 stops returning that endpoint on new DNS resolutions. Realistic RTO is **not instant** — it's bounded below by the health-check interval/failure threshold (roughly 30-90 seconds to be marked unhealthy) plus DNS TTL and resolver caching behavior on the client side (commonly another 60-300 seconds in practice, longer for clients or resolvers that ignore low TTLs). The honest number to state in an interview is "low single-digit minutes," not "seconds" or "zero-downtime."

**Node failure.** The Kubernetes scheduler reschedules affected pods onto healthy nodes; Karpenter/Cluster Autoscaler replaces the lost node; multi-AZ/multi-zone node placement means a single node (or even a full AZ/zone) loss removes only a fraction of total capacity, and the PodDisruptionBudget prevents that fraction from being too large during any voluntary rebalancing happening at the same time.

**Pod failure / crash loop.** The liveness probe restarts a hung container; the ReplicaSet maintains desired replica count regardless. A genuine crash loop (the process dying repeatedly, not just hanging) triggers a restart-count alarm rather than silently cycling forever unnoticed.

**Pipeline failure.** Required status checks block a broken PR from merging at all. A deploy that passes CI but fails its post-deploy smoke test triggers the automatic rollback described in Section 5. A Terraform apply that fails mid-run leaves the state lock held; the documented runbook is to verify no other operation is genuinely in-flight, then release the lock manually rather than force-deleting it as a first resort.

**Credential compromise.** Because CI holds no long-lived cloud credentials at all (Section 7), the most common real-world leak vector is already closed. If a specific IAM role or secret is nonetheless suspected compromised: rotate the secret immediately via Secrets Manager/Secret Manager, tighten or revoke the affected role's trust policy, and use CloudTrail/Cloud Audit Logs to trace exactly what that identity touched — which is only a tractable investigation *because* least-privilege scoping (Section 7) kept the blast radius small in the first place.

**Network outage (NAT/LB layer).** Production runs NAT gateways across multiple AZs specifically so a single NAT's failure doesn't cut off an entire AZ's egress; load balancer health checks automatically stop routing to any target group/backend that's failing, without any human intervention.

**Traffic spike.** The HPA scales pod replicas, Karpenter/Cluster Autoscaler scales nodes to match — both react to real-time metrics, so the resume claim of "handling variable traffic without manual intervention" is literally what's being exercised here. Rate limiting at the WAF/Cloud Armor layer distinguishes a legitimate spike (let it scale) from an abusive one (throttle it) rather than treating both identically.

**Terraform state corruption.** S3 and GCS both have object versioning enabled specifically so a corrupted or bad state write can be rolled back to a prior version; state locking (DynamoDB on AWS, GCS's native locking on GCP) prevents the concurrent-write race that causes corruption in the first place; scheduled drift-detection plans (Section 11) catch manual out-of-band console changes before they compound into a bigger reconciliation problem; and no secret value is ever stored in state — Terraform holds references (ARNs/resource IDs) to Secrets Manager/Secret Manager entries, not the secret values themselves, so a leaked state file is embarrassing but not a credential breach.

---

## 13. Scalability

**100 users.** Trivially handled by the floor of the design — `minReplicas: 2-3` on a couple of small nodes. No architectural change; this is roughly where "dev" and early "staging" traffic actually sits.

**1,000 users.** Still comfortably inside the same baseline. This is a reasonable steady-state assumption for "production, day one" — HPA rarely needs to scale past its minimum, and this is the load level real load testing (Section 17) should target first.

**10,000 users.** HPA begins actively scaling (roughly 5-15 replicas depending on request shape), Karpenter/Cluster Autoscaler add nodes to match. Still entirely single-region, multi-AZ — no new services are needed, though this is the point where caching (a CDN in front of any cacheable responses) starts being worth evaluating rather than being necessary yet.

**100,000 users — the design target.** Both clusters are sized and tested for this: enough replicas across enough nodes, autoscaling bounds wide enough to absorb daily/weekly variability without a human touching a dial, and this is the point where the resume bullet about "handling variable traffic without manual intervention" is genuinely, not aspirationally, true. A CDN in front of cacheable content and a caching layer in front of any future datastore become concrete recommendations rather than premature optimizations at this point.

**1,000,000 users.** Honestly, this is past where this specific architecture's assumptions hold without real changes: single-region-multi-AZ per cloud likely needs to become multi-region per cloud (Route 53 latency-based routing across multiple regional cluster deployments, not just multi-cloud failover), any future datastore needs a real distributed-scale story (read replicas at minimum, sharding or a globally-distributed database at this tier), trace sampling has to drop well below 100%, and — just as importantly — this is no longer a "small team" scale of system, it needs a dedicated platform/SRE function. Stating plainly where a design's assumptions stop holding is itself a senior-level signal; claiming this architecture handles a million users unmodified would not survive five minutes of interview follow-up questions.

---

## 14. Cost Optimization

**Keeping dev under $30/month — the actual math.** A single EKS cluster's control-plane fee alone is $0.10/hour, roughly **$73/month** — more than double the entire dev budget before a single node, NAT gateway, or load balancer is provisioned. A single NAT Gateway adds another **~$32.85/month** in hourly charges alone (us-east-1, plus per-GB data processing). Neither of those facts is a reason to compromise the budget — it's the reason **dev does not run a persistent EKS cluster or NAT gateway at all.** The AWS side of "dev" is a local `kind`/`k3d` cluster (free) plus `terraform validate`/`plan` (also free) run in CI against the real `modules/aws/*` code, proving correctness without paying to run it continuously.

GCP is structurally different here, and worth calling out explicitly: GKE's cluster-management fee is also $0.10/hour flat, **but every GCP billing account receives a $74.40/month credit that fully covers exactly one zonal Standard or Autopilot cluster's management fee.** That asymmetry — AWS has no equivalent waiver, GCP does — is the direct reason the dev environment's *one* real, always-on managed cluster lives on GCP rather than AWS: a single small zonal GKE cluster with one or two small (e2-small class) nodes costs only the node compute itself, roughly $6-8/month, with the control-plane fee effectively zero.

Rounding out the dev budget: a Route 53 hosted zone (~$0.50/month) plus negligible query/health-check charges; container image storage in ECR/Artifact Registry (pennies, with lifecycle policies purging untagged/old images); a small number of secrets in Secrets Manager/Secret Manager (a couple of dollars at most, with lower-sensitivity config kept in the free-tier SSM Parameter Store instead, per Section 3); CloudWatch/Cloud Monitoring free-tier allowances, which comfortably cover dev's actual log/metric volume. GitHub Actions costs **$0** if the repository is public — and for a portfolio project meant to be shown to hiring managers, a public repo is the right call anyway, so the CI/CD pipeline itself runs on unlimited free GitHub-hosted minutes rather than the 2,000 free minutes/month private-repo allowance. Realistic total: **roughly $10-15/month**, with headroom under the $30 ceiling.

**Production optimizations that should stay off until they're needed.** Multi-AZ redundant NAT gateways (each additional NAT is another ~$33/month — justified once an uptime SLA actually depends on it, not before). Reserved/Savings Plans or Committed Use Discounts (only worth committing to once traffic is predictable enough to forecast, not during initial buildout). Aggressive Spot/preemptible usage for a larger fraction of capacity (a real cost lever, but needs interruption-handling to be proven solid first — turning this on before that maturity just trades cost for reliability incidents). CDN caching in front of the load balancer (worth it once cacheable traffic volume justifies the setup, per Section 13's 10K-100K user range). WAF/Cloud Armor managed rule sets (a small recurring cost, worth turning on once the service is genuinely public-facing with real user data — not necessary for a demo hitting single-digit RPS). Extended log retention and 100%-sampling tracing (fine at low volume; both need to be tuned down — shorter retention tiers, lower trace sampling rate — well before Section 13's 100K-user mark, or the observability bill starts scaling faster than the product does).

---

## 15. Risks and Tradeoffs

**"Single reusable codebase" is DRY at the interface level, not the resource level.** Terraform's `aws_*` and `google_*` resources are genuinely different code — there is real, inherent duplication between the `modules/aws/*` and `modules/gcp/*` trees that no amount of clever module design fully eliminates. Tools like Terragrunt (to DRY up the repeated environment/root-module boilerplate) or a higher-level abstraction like Crossplane or Pulumi (which can in principle express both clouds' resources in one language with shared logic) would reduce this further, at the cost of an additional tool to learn and operate, or a genuine lock-in to that abstraction layer instead of to a single cloud. This tradeoff is real and unresolved by design, not an oversight — see Section 16 for how hard a skeptical reviewer should push on it.

**No persistent datastore is in scope.** `pulse-api` is stateless by design, which keeps this project focused on the infrastructure story it's meant to tell — but it also means the hardest part of real multi-cloud operation (keeping data consistent, replicated, or at least independently durable across two providers) is never actually exercised here. The moment a real service adds RDS/Cloud SQL or any stateful store, the HA and failover story in Sections 4 and 12 gets substantially harder, and that should be stated plainly rather than glossed over.

**Running full infrastructure on two clouds roughly doubles operational surface area** — two IAM models, two networking models, two monitoring backends to keep mentally in sync, for one simple stateless service. A real startup at this size and team scale would very plausibly choose one cloud plus a documented disaster-recovery runbook over true dual-cloud active operation. This project accepts the doubled surface deliberately, for the specific purpose of demonstrating multi-cloud capability — that's a legitimate reason for a portfolio piece and a much weaker one for an actual early-stage business, and the difference between those two contexts is worth being explicit about in an interview.

**Push-based CD, not GitOps.** GitHub Actions runs `helm upgrade` directly against cluster credentials it obtains just-in-time. That's simpler to operate today, but a pull-based GitOps controller (Argo CD/Flux) is generally considered more robust at scale — no CI system needs any cluster-write credential at all, and the cluster's actual state is continuously reconciled against Git rather than only updated at deploy time. Flagged here as accepted technical debt with a clear upgrade path (Section 17), not as an oversight.

**No service mesh.** Entirely appropriate for one service with no service-to-service traffic to secure or shape — but worth revisiting the moment this system grows past a single microservice into several that need mTLS, canary routing, or fine-grained traffic policy between them.

**Secrets rotation is basic.** Secrets Manager/Secret Manager support automatic rotation, but this design doesn't wire up rotation Lambdas/Cloud Functions on a schedule — secrets are stored securely and accessed via short-lived identity, but rotated manually. Real production hardening would automate this.

**Capacity claims need load-test evidence, not just architecture.** Everything in Section 13 is a design argument for why the platform *should* handle 100,000 users — it is not a substitute for actually running k6/Locust against it and measuring real p95/p99 latency and error rate under that load. Claiming a specific user-scale number without load-test data behind it is a common portfolio-project overreach, and Section 17's roadmap deliberately includes load testing as its own phase rather than an afterthought.

---

## 16. Principal Engineer Design Review

*Voice shift: the following is written from the perspective of a Principal Engineer at Microsoft conducting a production-readiness review. It is intentionally unsparing — the standard applied is "would I approve this to serve real paying customers," not "is this an impressive-looking portfolio project."*

Let's start with the question nobody in the design doc wants to answer directly enough: **why does one stateless CRUD-ish service need two entire Kubernetes clusters on two entire cloud providers?** The doc's answer — cross-provider failure isolation via Route 53 — is the correct answer in theory, and I'll credit that it's stated honestly instead of hidden behind "multi-cloud is best practice" hand-waving. But in practice: you've doubled your IAM surface, doubled your networking surface, doubled your on-call cognitive load, and doubled your attack surface, to protect against a full-provider-outage failure mode that, for a service with no stated multi-region-within-a-cloud story either, is nowhere near the top of your actual risk list. A regional AWS outage is rarer than "someone misconfigures a security group" or "a bad deploy ships a memory leak." If I were reviewing this for an actual product, I'd ask you to cut it to one cloud plus a *documented, periodically-tested* runbook for migrating to the second cloud under duress, and revisit true active-active only once you have a specific SLA number that demands it. For a portfolio project meant to demonstrate multi-cloud skill specifically, I'll allow the constraint — but say so explicitly when you present this, because I will ask, and "the prompt told me to" is a weaker answer than "here's the real DR value and here's its real cost," which is what's written above. Good that it's there. Not good enough that it's the *first* thing a reader has to dig for.

Second: the "single reusable Terraform codebase" framing, even hedged the way it is here, still oversells slightly. You've correctly avoided the rookie mistake of claiming one HCL resource spans both clouds. But if I hand this repo to a new hire, they're still maintaining two full sets of near-parallel modules by hand, and "near-parallel" drifts over time unless something enforces it — a linter, a schema test, *something* that fails CI when `modules/aws/network`'s outputs and `modules/gcp/network`'s outputs diverge in shape. Right now that parallelism is a convention, not a control. Either wire up an automated contract test, or bring in Terragrunt to at least eliminate the copy-pasted environment boilerplate you're currently maintaining by hand across `environments/*/aws` and `environments/*/gcp`. "We chose not to overengineer with Crossplane" is a defensible answer. "We have no automated check that our two module trees haven't silently diverged" is not.

Third, on IAM: I see the word "least privilege" a lot. Show me the actual policy JSON. Every team that tells me they've implemented least privilege has at least one `Resource: "*"` they forgot about, usually on the CI apply role because it was easier to get the pipeline green than to scope it correctly under deadline. I want IAM Access Analyzer (or Policy Simulator on the GCP side, or `cloudsplaining` in CI) running continuously against these policies, not just a design doc asserting scoping happened once. Least privilege is a property that decays; it needs a control that catches decay, not a one-time design decision.

Fourth: staging. You're describing staging as a real, presumably always-on EKS + GKE pair. Why? Staging exists to validate a deploy before production sees it — it does not need to be a 24/7 cost center. I'd push this to ephemeral: created by the pipeline before a deploy, torn down after the smoke test passes (or after a fixed TTL), and I'd want to see that as the default, not as a "future improvement" mentioned in passing. This is a cost and an attack-surface reduction sitting right there unclaimed.

Fifth: observability cost is treated a little too lightly given how often this actually blows up in the real world. "We kept metric cardinality low" is the right instinct, but I want to know who's watching the CloudWatch/Cloud Monitoring bill on a weekly basis, because Container Insights and custom-metric costs are exactly the kind of thing that creeps from "cheap" to "the biggest line item on the bill" over two quarters without a single change in traffic — just accumulated dashboard sprawl and well-intentioned engineers adding "just one more" high-cardinality label. I'd want a monthly observability-cost review as an explicit, named process, not an implicit hope.

Sixth: DNS failover RTO. I appreciate that this doc states a real number range instead of implying instant failover — that's more honest than most designs I review. But "low single-digit minutes" needs to actually be measured, with a health-check-failure-to-DNS-propagation test run on a schedule, not asserted from first principles about TTL. Untested RTO claims are just guesses with better formatting.

Seventh — and this is a smaller point but it'll come up — supply chain security lists Trivy, SBOM generation, and cosign/Sigstore, and to its credit the doc is honest that signing isn't yet *enforced* at admission. Good. Don't let that stay a "near-term hardening step" indefinitely — an unenforced control is a slide in a deck, not a control.

**Verdict:** approved for what this actually is — a portfolio project demonstrating specific, real infrastructure skills, built with more honesty about its own tradeoffs than most production systems I review get from their own teams. Not approved, as-is, to serve real customers without: (1) an explicit, quantified answer to "why dual-cloud" tied to a real SLA, (2) automated contract testing between the two module trees instead of convention, (3) continuous IAM policy verification instead of point-in-time design, (4) ephemeral staging by default, and (5) a named owner and cadence for the observability cost review. None of those five are hard to fix. All five are the difference between "looks production-grade" and "is production-grade," which is precisely the distinction I'm here to check for.

---

## 17. Implementation Roadmap

Each phase below is independently buildable and independently testable — no phase requires the next one to exist in order to demonstrate that it works.

**Phase 0 — Foundations.** Repository scaffolding, Terraform backend bootstrap (S3+DynamoDB, GCS bucket), branch protection rules, CODEOWNERS, a minimal GitHub Actions skeleton that only lints. *Test:* `terraform init` succeeds against both remote backends; an empty/trivial pipeline run goes green.

**Phase 1 — Containerized application.** Build `pulse-api`, its Dockerfile (multi-stage, distroless, non-root), `docker-compose` for fully local development, unit tests, and the `/healthz`/`/readyz`/`/metrics` endpoints. *Test:* the container builds and runs locally, health endpoints respond correctly, unit tests pass in CI.

**Phase 2 — Networking and cluster provisioning (staging first).** Write and apply `modules/aws/network`, `modules/aws/eks-cluster`, `modules/gcp/network`, `modules/gcp/gke-cluster`; provision real staging clusters on both clouds. *Test:* `kubectl get nodes` succeeds against both clusters using the kubeconfig Terraform outputs.

**Phase 3 — Kubernetes application deployment.** Write the Helm chart / `modules/kubernetes/*` (Deployment, Service, Ingress, HPA, PDB); deploy manually to staging on both clouds. *Test:* hitting the ALB URL and the GCLB URL both return a healthy response from the actual application.

**Phase 4 — CI/CD automation.** Wire up GitHub Actions OIDC to both clouds, the build-once/dual-push image pipeline, automatic staging deploys on merge, post-deploy smoke tests, and automatic rollback on smoke-test failure. *Test:* a merge to `main` auto-deploys to staging and passes its smoke test; a deliberately broken build triggers an automatic rollback within the pipeline.

**Phase 5 — Secrets and IAM hardening.** Migrate any placeholder config into Secrets Manager/Secret Manager, implement IRSA/Workload Identity roles scoped per-service-account, remove every standing credential. *Test:* no long-lived cloud credential exists anywhere in the repo or CI configuration; a policy-scanning tool (cloudsplaining/Policy Simulator) reports zero wildcard-resource findings.

**Phase 6 — Observability.** OpenTelemetry instrumentation in the application, the Collector DaemonSet, CloudWatch and Cloud Monitoring dashboards, alerting policies routed to a shared Slack channel. *Test:* deliberately inducing an error spike or latency spike triggers the expected alert within the expected time window; dashboards visibly reflect real generated traffic.

**Phase 7 — Production environment and cross-cloud failover.** Provision production clusters at HA sizing (multi-AZ/zone, tuned autoscaling bounds, tightened PDBs), wire up Route 53 health-checked routing across both clouds, and run real load tests (k6/Locust) against the target traffic profile. *Test:* the load test hits the target RPS at acceptable p95 latency with zero manual intervention; a simulated failure of one cloud's health check triggers Route 53 failover within the RTO measured (not assumed) in testing.

**Phase 8 — Cost and security review pass.** Run tfsec/checkov/cloudsplaining end to end, review actual CloudWatch/Cloud Monitoring spend against the design's cost claims, tune log retention and trace sampling, and write up the ADRs and runbooks that should have been accumulating all along. *Test:* the security scan reports zero high/critical findings; actual billing for a full month falls inside the ranges claimed in Section 14.

**Phase 9 — Stretch goals, explicitly out of scope for the core resume bullets.** GitOps migration (Argo CD/Flux) replacing push-based deploys; a service-mesh evaluation, only if/when the system grows past one microservice; genuine multi-region-within-a-cloud expansion; and chaos engineering (Chaos Mesh or similar) to empirically validate the resilience claims in Section 12 instead of only architecting for them. Marked as stretch specifically so it's clear these are maturity steps *beyond* what the four target resume bullets require — worth building for a portfolio that wants to go further, not required to make the initial claims true.
