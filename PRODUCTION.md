# Production Setup Guide

Step-by-step guide to deploying PodDoctor in production with high availability, monitoring, GitOps integration, and security hardening. Helm is the primary supported deployment method; a kustomize path exists for pure-GitOps shops that don't run Helm.

## Prerequisites

| Requirement | Minimum | Recommended |
|------------|---------|-------------|
| Kubernetes | 1.28 | 1.30+ |
| Helm | 3.12 | Latest |
| kubectl | 1.28 | Latest |
| Container registry | Any (ghcr, ECR, GCR, ACR) | Private with scanning |
| Go (build only) | 1.26 | 1.26 |
| Node.js (build only, for the dashboard SPA) | 24 | 24 |

## Step 1: Get the Container Image

Tagged releases (`git tag v0.1.0 && git push --tags`) trigger `.github/workflows/release.yml`, which builds and pushes a multi-arch (`linux/amd64`, `linux/arm64`) image to `ghcr.io/<org>/poddoctor:<version>` and publishes the Helm chart as an OCI artifact to `oci://ghcr.io/<org>/charts` — pull it directly instead of building locally:

```bash
docker pull ghcr.io/your-org/poddoctor:v0.1.0
```

To build locally instead (e.g. to test an unreleased change):

```bash
git clone https://github.com/chenar/poddoctor.git
cd poddoctor

docker build -t ghcr.io/your-org/poddoctor:v0.1.0 .
docker push ghcr.io/your-org/poddoctor:v0.1.0
```

For air-gapped environments:
```bash
docker save ghcr.io/your-org/poddoctor:v0.1.0 -o poddoctor-v0.1.0.tar
# transfer tar to air-gapped network
docker load -i poddoctor-v0.1.0.tar
```

## Step 2: Install via Helm

```bash
helm upgrade --install poddoctor charts/poddoctor \
  --namespace poddoctor-system --create-namespace \
  --set image.repository=ghcr.io/your-org/poddoctor \
  --set image.tag=v0.1.0 \
  --wait
```

This installs the `PodDiagnosis` CRD (with `helm.sh/resource-policy: keep`, so it survives `helm uninstall`), a `ClusterRole`/`ClusterRoleBinding` scoped only to Pods/Events/ReplicaSets/Deployments (read) and `poddiagnoses` (full), and the operator Deployment.

## Step 3: Verify Installation

```bash
kubectl -n poddoctor-system get pods
# NAME                          READY   STATUS    RESTARTS   AGE
# poddoctor-7d4b8f6c9-x2k4p     1/1     Running   0          10s

kubectl get crd poddiagnoses.diagnostics.poddoctor.dev

# Trigger a real failure and watch it get diagnosed
kubectl apply -f config/samples/demo-oomkilled.yaml
kubectl get pd demo-oomkilled -w
# NAME             POD              ROOT CAUSE   CONFIDENCE   RESTARTS   AGE
# demo-oomkilled   demo-oomkilled   OOMKilled    High         1          38s

kubectl delete -f config/samples/demo-oomkilled.yaml
```

## Step 4: High Availability

```bash
helm upgrade poddoctor charts/poddoctor -n poddoctor-system \
  --reuse-values \
  --set replicaCount=2 \
  --set leaderElection.enabled=true \
  --set podDisruptionBudget.enabled=true \
  --set podDisruptionBudget.minAvailable=1
```

`leaderElection.enabled` defaults to `true` already — it just needs `replicaCount > 1` to matter. Only the leader reconciles; standbys hot-wait, so failover is a leader-lease handoff, not a cold start.

## Step 5: Restrict Watch Scope (optional)

By default PodDoctor watches Pods cluster-wide (needed for most real deployments — crashes happen in app namespaces, not just its own). To restrict it to specific namespaces instead (comma-separated):

```bash
helm upgrade poddoctor charts/poddoctor -n poddoctor-system --reuse-values \
  --set watchNamespace=my-app-namespace
# or several:
helm upgrade poddoctor charts/poddoctor -n poddoctor-system --reuse-values \
  --set watchNamespace="team-a,team-b,team-c"
```

This also bounds memory: the controller's Pod cache holds every watched Pod object in memory, so on a very large cluster (100k+ pods), narrowing `watchNamespace` to the namespaces that actually matter is worth doing even before RBAC concerns.

Note: the ClusterRole is unchanged either way (cluster-wide read access is still granted) — this only restricts what the controller's cache watches, not what it's permitted to see. If you need namespace-scoped RBAC too, swap the Helm chart's ClusterRole/ClusterRoleBinding for a Role/RoleBinding in that namespace manually.

## Step 6: Network Policy (Zero-Trust)

The operator only needs to reach the Kubernetes API server (for watches, log fetches, and event reads) — no other egress, unless `notifications.webhookURL`/`notifications.routes` is configured, in which case add an egress rule for that destination (or the fleet hub's Service, if internal to the cluster):

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: poddoctor-operator
  namespace: poddoctor-system
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: poddoctor
  policyTypes:
    - Ingress
    - Egress
  ingress:
    - ports:
        - port: 8081   # health probes (kubelet)
          protocol: TCP
        - port: 8080   # metrics (Prometheus)
          protocol: TCP
  egress:
    - to:
        - ipBlock:
            cidr: 0.0.0.0/0
      ports:
        - port: 443
          protocol: TCP
        - port: 6443
          protocol: TCP
```

## Step 7: Resource Quotas

```yaml
apiVersion: v1
kind: ResourceQuota
metadata:
  name: poddoctor-quota
  namespace: poddoctor-system
spec:
  hard:
    pods: "4"
    requests.cpu: "200m"
    requests.memory: "256Mi"
    limits.cpu: "600m"
    limits.memory: "1Gi"
```

## Step 8: Monitoring (Prometheus)

Enable the ServiceMonitor if you run the Prometheus Operator:

```bash
helm upgrade poddoctor charts/poddoctor -n poddoctor-system --reuse-values \
  --set metrics.serviceMonitor.enabled=true
```

PodDoctor exposes controller-runtime's standard metrics — reconcile counts/errors/duration and workqueue depth — plus standard Go/process metrics, and one business metric: `poddoctor_diagnoses_total{root_cause,confidence}`, a counter incremented on every new diagnosis. It survives `PodDiagnosis` CRs being garbage-collected with their pods, so it's the right thing to alert and trend on, not the CR count.

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: poddoctor-alerts
  namespace: monitoring
spec:
  groups:
    - name: poddoctor
      rules:
        - alert: PodDoctorReconcileErrors
          expr: |
            rate(controller_runtime_reconcile_errors_total{controller="poddiagnosis"}[10m]) > 0
          for: 5m
          labels:
            severity: warning
          annotations:
            summary: "PodDoctor is failing to reconcile ({{ $value }} errors/s)"

        - alert: PodDoctorDown
          expr: |
            kube_deployment_status_replicas_available{
              namespace="poddoctor-system"
            } == 0
          for: 2m
          labels:
            severity: critical
          annotations:
            summary: "PodDoctor has no available replicas — crash loops are going undiagnosed"

        - alert: HighConfidenceCrashLoopDetected
          expr: |
            increase(poddoctor_diagnoses_total{confidence="High"}[5m]) > 0
          for: 0m
          labels:
            severity: warning
          annotations:
            summary: "PodDoctor made {{ $value }} new high-confidence diagnosis/diagnoses in the last 5m"

        - alert: OOMKilledRateHigh
          expr: |
            increase(poddoctor_diagnoses_total{root_cause="OOMKilled"}[30m]) > 5
          for: 0m
          labels:
            severity: warning
          annotations:
            summary: "{{ $value }} OOMKilled diagnoses in the last 30m — check for a memory leak or undersized limits"
```

Alternatively, for a per-object view: `kubectl get pd -A` or the built-in dashboard (`svc/<release>-dashboard`) show live diagnoses without needing kube-state-metrics' CRD support configured.

## Step 9: GitOps Integration

### FluxCD (HelmRelease)

```yaml
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: poddoctor
  namespace: flux-system
spec:
  interval: 10m
  url: https://github.com/your-org/poddoctor
  ref:
    tag: v0.1.0
---
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: poddoctor
  namespace: poddoctor-system
spec:
  interval: 10m
  chart:
    spec:
      chart: charts/poddoctor
      sourceRef:
        kind: GitRepository
        name: poddoctor
        namespace: flux-system
  values:
    image:
      repository: ghcr.io/your-org/poddoctor
      tag: v0.1.0
    replicaCount: 2
    podDisruptionBudget:
      enabled: true
```

### ArgoCD

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: poddoctor
  namespace: argocd
spec:
  project: platform
  source:
    repoURL: https://github.com/your-org/poddoctor
    targetRevision: v0.1.0
    path: charts/poddoctor
    helm:
      values: |
        image:
          repository: ghcr.io/your-org/poddoctor
          tag: v0.1.0
        replicaCount: 2
  destination:
    server: https://kubernetes.default.svc
    namespace: poddoctor-system
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
```

Add a custom health check to `argocd-cm` so PodDoctor's own Deployment health reflects correctly (standard Deployment health check already covers this — no custom Lua needed, since PodDiagnosis objects are outputs, not something ArgoCD manages).

## Step 10: Multi-Cluster Deployment

```
┌───────────────────┐     ┌───────────────────┐     ┌───────────────────┐
│  Dev Cluster       │     │  Staging Cluster   │     │  Production        │
│  PodDoctor ✓       │     │  PodDoctor ✓       │     │  PodDoctor ✓        │
└───────────────────┘     └────────────────────┘     └───────────────────┘
         │                          │                          │
         └──────────────────────────┼──────────────────────────┘
                                     │
                            ┌────────▼────────┐
                            │   Git Repo      │
                            │  (Helm values   │
                            │  per cluster)   │
                            └─────────────────┘
```

Same chart everywhere, per-cluster `values-<env>.yaml` overrides (image tag, replica count, watchNamespace, clusterName). Each cluster diagnoses its own failures independently — the diagnosis engine never talks cross-cluster.

### Fleet-wide view (optional)

Diagnosing is per-cluster, but *seeing* what's happening across a datacenter's worth of clusters shouldn't require N separate `kubectl get pd` sessions. Deploy `charts/poddoctor-hub` once, centrally (a management cluster, or wherever you'd run any other shared internal service), and point every cluster's `notifications.routes` (or plain `webhookURL`) at it:

```
┌───────────────┐  ┌───────────────┐  ┌───────────────┐
│  Dev Cluster   │  │  Staging       │  │  Production    │
│  PodDoctor     │  │  PodDoctor     │  │  PodDoctor     │
└───────┬────────┘  └───────┬────────┘  └───────┬────────┘
        │ webhook           │ webhook           │ webhook
        └───────────────────┼───────────────────┘
                             ▼
                   ┌───────────────────┐
                   │  poddoctor-hub     │  (management cluster)
                   │  + Postgres        │
                   └───────────────────┘
```

See [README.md § Fleet Hub](README.md#fleet-hub-multi-cluster) for the install command. It's stateless HTTP in front of Postgres — no Kubernetes RBAC, so it doesn't need to run *in* any of the clusters it aggregates.

## Step 11: Backup & Recovery

The operator is stateless — all state lives in the `PodDiagnosis` CRs themselves, which etcd already backs up as part of normal cluster backup:

```bash
# If the operator dies, just redeploy — no state to restore
helm upgrade --install poddoctor charts/poddoctor -n poddoctor-system --reuse-values

# Existing PodDiagnosis objects are untouched; new failures get diagnosed
# as soon as the new pod starts reconciling.
```

## Step 12: Upgrade Procedure

```bash
docker build -t ghcr.io/your-org/poddoctor:v0.2.0 .
docker push ghcr.io/your-org/poddoctor:v0.2.0

helm upgrade poddoctor charts/poddoctor -n poddoctor-system \
  --reuse-values --set image.tag=v0.2.0 --wait

kubectl -n poddoctor-system rollout status deployment/poddoctor
```

If the new version changes the CRD schema, `installCRDs: true` (default) means `helm upgrade` applies it automatically — Helm diffs and patches CRDs on upgrade the same as any other templated resource (this only applies to CRDs rendered from `templates/`, which is why this chart deliberately does *not* use Helm's separate `crds/` directory convention).

## Security Checklist

| Item | Status | Notes |
|------|--------|-------|
| Container runs as non-root | ✅ | UID 65532 |
| Read-only filesystem | ✅ | `readOnlyRootFilesystem: true` |
| No privilege escalation | ✅ | `allowPrivilegeEscalation: false` |
| Capabilities dropped | ✅ | `drop: ALL` |
| Seccomp profile | ✅ | `RuntimeDefault` |
| Distroless base image | ✅ | `gcr.io/distroless/static:nonroot` |
| No secrets access | ✅ | RBAC grants no `secrets` verbs at all |
| RBAC is read-mostly | ✅ | Only write access is to its own `poddiagnoses` CRD + Events |
| No network egress (except API server) | ✅ | NetworkPolicy applied (Step 6) |
| Pod Security Standards: restricted | ✅ | Passes `restricted` profile |
| Image signed (optional) | ⬜ | Add cosign in CI |
| SBOM generated (optional) | ⬜ | Add syft in CI |
| Vulnerability scanned | ⬜ | Add trivy scan in CI |

PodDoctor reads Pod logs (`pods/log`) and Kubernetes Events cluster-wide — this is log/metadata read access, not `secrets` or `exec`. If your threat model treats crash logs as sensitive (they can contain leaked credentials from misconfigured apps), restrict `watchNamespace` (Step 5) rather than relying on RBAC alone, since the ClusterRole itself doesn't scope by namespace.

## Troubleshooting

### Operator Not Starting

```bash
kubectl -n poddoctor-system logs deployment/poddoctor
kubectl -n poddoctor-system describe pod -l app.kubernetes.io/name=poddoctor
```

### A Known Crash Loop Isn't Getting a PodDiagnosis

```bash
# Confirm the container status actually shows a trigger reason
kubectl get pod <pod> -o jsonpath='{.status.containerStatuses[*].state.waiting.reason}'
# Must be one of: CrashLoopBackOff, ImagePullBackOff, ErrImagePull, InvalidImageName

# Check the operator's own logs for that pod name
kubectl -n poddoctor-system logs deployment/poddoctor | grep <pod-name>

# Confirm RBAC allows it to read logs/events for that namespace
kubectl auth can-i get pods/log --subresource=log \
  --as=system:serviceaccount:poddoctor-system:poddoctor
```

### CRD Not Found

```bash
kubectl get crd poddiagnoses.diagnostics.poddoctor.dev
# If missing (e.g. installCRDs was set to false):
kubectl apply -f charts/poddoctor/templates/crd.yaml
```

### RBAC Permission Denied

```bash
kubectl auth can-i update poddiagnoses/status \
  --as=system:serviceaccount:poddoctor-system:poddoctor
# Should return "yes"
```

## Production Readiness Checklist

- [ ] Container image built and pushed to private registry
- [ ] Image tag is immutable (never `:latest` in production)
- [ ] HA enabled (`replicaCount >= 2`, `leaderElection.enabled=true`)
- [ ] Network policy applied
- [ ] PodDisruptionBudget enabled
- [ ] Prometheus alerts configured (reconcile errors, deployment down)
- [ ] ServiceMonitor enabled if running Prometheus Operator
- [ ] `watchNamespace` decision made deliberately (cluster-wide vs. scoped)
- [ ] Tested upgrade procedure (`helm upgrade`, verify rollout)
- [ ] Tested failure scenario (kill operator pod, confirm redeploy recovers with no data loss)
- [ ] Pod Security Standards validated (`restricted`)
- [ ] Ran the three demo failures (`config/samples/demo-*.yaml`) end to end and confirmed correct root causes
