# PodDoctor

**Root-cause diagnosis for CrashLoopBackOff, done in-cluster, automatically.**

A Kubernetes Operator that watches for pods stuck in `CrashLoopBackOff` / `ImagePullBackOff`, correlates exit codes, kubelet-reported reasons, recent Kubernetes Events, the crashed container's previous logs, and rollout timing — then writes a human-readable root cause, confidence level, and recommendation to a `PodDiagnosis` custom resource. No more `kubectl describe`, `kubectl logs --previous`, and `kubectl rollout history` stitched together by hand at 3am.

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8.svg)](https://go.dev)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-1.28+-326CE5.svg)](https://kubernetes.io)

---

## Why This Exists

`kubectl describe pod` and `kubectl logs --previous` show you raw evidence. Nothing in the ecosystem synthesizes it into an answer:

| Tool | What it does | Root-causes the failure for you? |
|------|--------------|-----------------------------------|
| `kubectl describe pod` | Shows events, last state, exit code | No — you read and interpret it |
| `kubectl logs --previous` | Shows the crashed container's logs | No — you grep for the error yourself |
| Lens / k9s | Nicer UI over the same raw data | No — still manual correlation |
| Sentry / error trackers | App-level exceptions, if instrumented | No — misses infra causes (OOM, image pull, probe misconfig) |
| **PodDoctor** ✦ | **Correlates exit code + events + logs + rollout timing → root cause** | **Yes — written to a CR the moment it happens** |

PodDoctor doesn't replace any of the above — it's the layer that turns "container app-7d4b8f6c9-x2k4p is crash-looping" into "OOMKilled, High confidence, raise memory limits" without a human doing the correlation.

### How Diagnosis Works

```
Pod enters CrashLoopBackOff / ImagePullBackOff
              │
              ▼
  ┌─────────────────────────┐
  │   Gather Evidence        │
  │  • waiting/terminated    │
  │    state, exit code      │
  │  • recent Events         │
  │  • previous container    │
  │    logs (tail)           │
  │  • owning Deployment's   │
  │    rollout timing        │
  └────────────┬─────────────┘
               ▼
  ┌─────────────────────────┐
  │  Rule-Based Engine        │   deterministic, offline,
  │  (internal/diagnosis)     │   zero external API calls
  └────────────┬─────────────┘
               ▼
   PodDiagnosis CR (status: rootCause, confidence,
   summary, recommendation, evidence) + a Kubernetes
   Event on the Pod
```

Diagnosis is a pure, deterministic rule engine — no LLM call, no network egress, works air-gapped. See [`internal/diagnosis/rules.go`](internal/diagnosis/rules.go) for the full precedence order.

---

## Quick Start (Helm)

```bash
helm upgrade --install poddoctor charts/poddoctor \
  --namespace poddoctor-system --create-namespace \
  --wait
```

That's it — CRD, RBAC, and the operator Deployment are all installed. See it work:

```bash
kubectl apply -f config/samples/demo-oomkilled.yaml
kubectl get pd demo-oomkilled -w
```

```
NAME             POD              ROOT CAUSE   CONFIDENCE   RESTARTS   AGE
demo-oomkilled   demo-oomkilled   OOMKilled    High         1          41s
```

```bash
kubectl describe pd demo-oomkilled
```

```
Status:
  Root Cause:      OOMKilled
  Confidence:      High
  Summary:         Container exceeded its memory limit and was killed by the kernel OOM killer.
  Recommendation:  Raise resources.limits.memory, or investigate a possible memory leak in the application.
  Exit Code:       137
  Restart Count:   1
```

Or skip `kubectl` entirely and look at the built-in dashboard — a small read-only page listing every diagnosed crash, newest first:

```bash
kubectl -n poddoctor-system port-forward svc/poddoctor-dashboard 8082:8082
open http://localhost:8082
```

Remove it:

```bash
helm uninstall poddoctor -n poddoctor-system
# CRD (and every PodDiagnosis) is kept — see charts/poddoctor/values.yaml `installCRDs`
```

---

## Deployment Methods

| Method | Best for | Command |
|--------|----------|---------|
| **Helm** | Production, upgrades, GitOps (Flux `HelmRelease`, ArgoCD) | `helm upgrade --install poddoctor charts/poddoctor -n poddoctor-system --create-namespace` |
| **Kustomize** | GitOps shops that don't run Helm | `kubectl apply -k config/crd && kubectl apply -k config/default` |

See [`PRODUCTION.md`](PRODUCTION.md) for HA, network policy, monitoring, and GitOps integration in detail.

---

## What Triggers a Diagnosis

The controller watches Pods cluster-wide (configurable, see below) and reconciles only when a container's waiting-state reason is one of:

- `CrashLoopBackOff`
- `ImagePullBackOff` / `ErrImagePull`
- `InvalidImageName`

This applies to **init containers as well as regular containers** — a migration/setup init container stuck looping is diagnosed just like the main container. Each failing container in a pod gets its own `PodDiagnosis` (see [Custom Resource](#custom-resource)), so a sidecar crash-looping alongside a healthy main container is still caught.

Each restart episode is diagnosed once (deduped on restart count) and re-diagnosed only when the restart count advances — so a pod stuck looping doesn't spam reconciles or rewrite its `PodDiagnosis` every resync.

## Root Causes Detected

| Root Cause | Signal | Confidence |
|------------|--------|------------|
| `OOMKilled` | Kubelet `OOMKilled` reason, or exit 137 + log text | High / Medium |
| `ImagePullError` | `ImagePullBackOff` / `ErrImagePull` / `InvalidImageName` | High |
| `BadCommand` | Exit 126 (not executable) / 127 (not found) | High |
| `SegFault` | Exit 139 (SIGSEGV) | High |
| `SignalKilled` | Exit 143 (SIGTERM) | Medium |
| `ProbeFailure` | Recent `Unhealthy`/"probe failed" Event alongside a kill | High |
| `RecentRolloutRegression` | Pod started within the rollout-correlation window of a new ReplicaSet (Deployments) or ControllerRevision (StatefulSets/DaemonSets) | Medium |
| `ApplicationError` | Any other non-zero exit | Medium |
| `Unknown` | No matching signature | Low |

Rollout timing is layered onto whichever primary cause is found (e.g. "OOMKilled — and also started right after a rollout"), or becomes the lead cause itself when nothing else matches.

Node conditions are layered on too: if the pod's node currently reports `MemoryPressure`, `DiskPressure`, `PIDPressure`, or isn't `Ready`, that's noted in the summary — and an `OOMKilled` diagnosis on a node under `MemoryPressure` is upgraded to High confidence with a different recommendation ("check node allocatable memory and workload density" instead of "raise this pod's limits"), since the fix usually isn't in the pod spec.

---

## Custom Resource

`PodDiagnosis` objects are **created and owned by the operator** — one per crash-looping *container* (named `<pod>-<container>`, so a pod with two failing containers gets two objects), garbage-collected automatically when the pod is deleted (via `ownerReferences`). You don't write these; you read them.

```yaml
apiVersion: diagnostics.poddoctor.dev/v1alpha1
kind: PodDiagnosis
metadata:
  name: payments-api-7d4b8f6c9-x2k4p-app
  namespace: platform
  ownerReferences:
    - apiVersion: v1
      kind: Pod
      name: payments-api-7d4b8f6c9-x2k4p
      controller: true
spec:
  podName: payments-api-7d4b8f6c9-x2k4p
  podNamespace: platform
  containerName: app
status:
  phase: Diagnosed
  rootCause: OOMKilled
  confidence: High
  summary: Container exceeded its memory limit and was killed by the kernel OOM killer.
  recommendation: Raise resources.limits.memory, or investigate a possible memory leak in the application.
  exitCode: 137
  terminationReason: OOMKilled
  restartCount: 4
  recentEvents:
    - reason: BackOff
      message: "Back-off restarting failed container"
      count: 4
  logExcerpt: "...tail of the crashed container's previous logs..."
  firstObserved: "2026-07-16T10:02:11Z"
  lastObserved: "2026-07-16T10:14:03Z"
```

### kubectl columns

```
$ kubectl get pd -A
NAMESPACE   NAME                                 POD                              ROOT CAUSE      CONFIDENCE   RESTARTS   AGE
platform    payments-api-7d4b8f6c9-x2k4p-app     payments-api-7d4b8f6c9-x2k4p     OOMKilled       High         4          12m
```

Short name: `pd`

---

## Configuration

| Helm value | Default | Description |
|------------|---------|--------------|
| `replicaCount` | `1` | Set ≥2 with `leaderElection.enabled=true` for HA |
| `watchNamespace` | `""` (all namespaces) | Comma-separated namespaces to restrict the operator's Pod watch to |
| `clusterName` | `""` | Identifies this cluster in outbound notifications — set it when feeding a fleet hub |
| `diagnosis.logTailLines` | `50` | Lines of previous-container-instance logs fetched as evidence |
| `diagnosis.rolloutWindow` | `10m` | How soon after a rollout a pod start still counts as rollout-correlated |
| `diagnosis.evidenceQPS` | `20` | Max apiserver requests/sec spent gathering evidence — caps self-inflicted load during a mass crash-loop |
| `diagnosis.kubeAPIQPS` / `diagnosis.kubeAPIBurst` | `50` / `100` | QPS/burst for the underlying Kubernetes client. client-go defaults to 5 QPS if left unset, which would throttle the controller below `evidenceQPS` itself |
| `metrics.serviceMonitor.enabled` | `false` | Create a `ServiceMonitor` for the Prometheus Operator |
| `dashboard.enabled` | `true` | Serve the read-only HTML dashboard (`svc/<release>-dashboard`) |
| `notifications.webhookURL` | `""` (disabled) | POST a notification for every new diagnosis — generic JSON or a Slack incoming webhook |
| `notifications.webhookFormat` | `generic` | `generic` (JSON fields) or `slack` (Slack message text) |
| `notifications.webhookToken` | `""` | Bearer token sent with `webhookURL` (e.g. a fleet hub's ingest token); passed via a Secret + env var, never a plain arg |
| `notifications.groupWindow` | `2m` | Fold repeated diagnoses with the same namespace+root-cause into one notification within this window |
| `notifications.routes` | `[]` | Route different namespaces to different webhooks instead of one global `webhookURL` — see [Notifications](#notifications) |
| `podDisruptionBudget.enabled` | `false` | Create a PDB (recommended once `replicaCount > 1`) |
| `installCRDs` | `true` | Render the CRD as part of this release (upgradable; `helm.sh/resource-policy: keep` protects it from `helm uninstall`) |

Full list in [`charts/poddoctor/values.yaml`](charts/poddoctor/values.yaml).

---

## Monitoring

PodDoctor exposes controller-runtime's standard metrics (reconcile count/errors/duration, workqueue depth) plus its own `poddoctor_diagnoses_total{root_cause,confidence}` counter on `:8080/metrics` — a durable trend of what's been failing even after the underlying `PodDiagnosis` CRs are garbage-collected with their pods. See [`PRODUCTION.md`](PRODUCTION.md) for ready-to-use `PrometheusRule` alerts.

## Notifications

Set `notifications.webhookURL` (Helm) or `--webhook-url` (flag) to get a best-effort POST for every new diagnosis — a generic JSON body by default, or Slack-formatted text with `notifications.webhookFormat=slack` / `--webhook-format=slack` pointed at a Slack incoming webhook URL. Notification failures are logged, not retried, and never fail the diagnosis itself.

**Alert grouping.** A rollout that crash-loops hundreds of pods with the same root cause doesn't send hundreds of notifications: repeated diagnoses sharing a `(namespace, rootCause)` within `notifications.groupWindow` (default 2m) are folded into the next notification for that key as a `suppressedCount`, so you get "OOMKilled in payments (+47 more like this)" instead of 48 separate alerts.

**Per-namespace routing.** For more than one destination — different teams' Slack channels, or a mix of team channels plus a [fleet hub](#fleet-hub-multi-cluster) — set `notifications.routes` instead of a single `webhookURL`:

```yaml
notifications:
  routes:
    - namespaces: ["payments", "checkout"]
      webhookURL: "https://hooks.slack.com/services/..."
      webhookFormat: slack
    - namespaces: ["*"]          # catch-all fallback
      webhookURL: "https://poddoctor-hub.mgmt.example.com/ingest"
      webhookFormat: generic
      webhookToken: "..."
```

Routes are checked in order, first match wins; `webhookURL`/`webhookFormat` at the top level become the fallback (`defaultWebhookURL`) if nothing matches. This renders as a Secret (routes may carry tokens) mounted at `/etc/poddoctor/notify-config.yaml` and passed via `--notify-config`, which takes over from `--webhook-url` entirely.

## Fleet Hub (multi-cluster)

A single global webhook doesn't give a fleet-wide view across many clusters. `cmd/hub` (chart: `charts/poddoctor-hub`) is a small central service, backed by Postgres, that every cluster's PodDoctor can point a notification route at:

```bash
helm install poddoctor-hub charts/poddoctor-hub -n poddoctor-hub --create-namespace \
  --set database.dsn="postgres://user:pass@your-postgres:5432/poddoctor?sslmode=require" \
  --set auth.token="$(openssl rand -hex 32)"
```

Then on each cluster's PodDoctor, add a catch-all route (or set it as the plain `webhookURL`) pointing at `http://<hub-service>.<namespace>.svc:8090/ingest` with the same token, and set `clusterName` so the hub can tell clusters apart. `helm install poddoctor-hub` prints the exact snippet in its `NOTES.txt`.

The hub exposes:
- `POST /ingest` — what each cluster's PodDoctor posts to (bearer-token authenticated).
- `GET /api/diagnoses?cluster=&namespace=&rootCause=&limit=` — JSON, for building your own views.
- `GET /` — an HTML dashboard across every cluster, with the same filters as query params.

It doesn't touch the Kubernetes API at all (no RBAC needed) — it's a plain HTTP service in front of Postgres. Not exposed outside the cluster by default; enable `ingress.enabled` if clusters need to reach it over the internet rather than a private network.

---

## Development

```bash
task build          # Build operator binary
task test            # Run all unit tests
task test:e2e         # Run E2E suite against a deployed operator
task docker:build     # Build container image
task helm:lint         # Lint the Helm chart
task helm:template     # Render the Helm chart locally
task helm:install      # helm upgrade --install into current cluster
task crd:diff           # Fail if the Helm CRD template drifted from config/crd/bases
task demo               # One command: kind cluster → build → helm install → real crash loops → results
task demo:clean          # Tear down the demo kind cluster
task generate             # Regenerate deepcopy methods via controller-gen
```

See [`TESTING.md`](TESTING.md) for the full test matrix and [`PRODUCTION.md`](PRODUCTION.md) for the production deployment guide.

---

## Project Structure

```
├── api/v1alpha1/            PodDiagnosis CRD types + hand-maintained deepcopy
├── cmd/main.go               Operator entrypoint (manager, leader election, flags)
├── cmd/hub/                   Fleet hub entrypoint (see charts/poddoctor-hub)
├── internal/diagnosis/       Pure rule-based root-cause engine (unit tested, no cluster deps)
├── internal/dashboard/       Small read-only HTML page over PodDiagnosis (stdlib html/template only)
├── internal/controller/      Reconciler: watches Pods, gathers evidence, writes PodDiagnosis
├── internal/notify/           Webhook sending + per-namespace routing
├── internal/alertgroup/        Storm dedup for repeated same-cause diagnoses
├── internal/metrics/            poddoctor_diagnoses_total registration
├── internal/hub/                  Fleet hub: Postgres store + HTTP API/dashboard
├── config/crd/                CRD manifest (kustomize path)
├── config/rbac/                ServiceAccount, ClusterRole, ClusterRoleBinding
├── config/manager/              Deployment (kustomize path)
├── config/samples/               Demo pods that reliably trigger each root cause
├── charts/poddoctor/              Helm chart (recommended deployment method)
├── charts/poddoctor-hub/            Helm chart for the optional fleet hub
├── hack/                            E2E test script, boilerplate
├── Dockerfile                        Multi-stage, distroless, non-root (operator)
├── Dockerfile.hub                     Same, for the fleet hub
├── Taskfile.yaml                      All automation
└── go.mod
```

---

## Security Posture

| Property | Detail |
|----------|--------|
| Container | Distroless, non-root (UID 65532), read-only fs, drop ALL caps, seccomp `RuntimeDefault` |
| RBAC | No `secrets` access at all. Read-only on Pods/Nodes/Events/ReplicaSets/Deployments/ControllerRevisions; write access limited to its own `poddiagnoses` CRD and Events |
| Network | Egress is to the Kubernetes API server only, unless notifications are configured, which adds egress to whatever webhook(s)/fleet hub are set |
| Fleet hub | Separate service (`charts/poddoctor-hub`), no Kubernetes RBAC at all — plain HTTP + Postgres, gated by a shared bearer token |
| HA | `leaderElection.enabled=true` (default), `replicaCount ≥ 2` |
| Diagnosis engine | Fully offline, rule-based — no external API calls, works air-gapped |

---

## Tech Stack

| Component | Version |
|-----------|---------|
| Go | 1.26+ |
| Kubebuilder layout | go.kubebuilder.io/v4 |
| controller-runtime | 0.24.1 |
| Kubernetes client-go | 0.36.0 |
| Kubernetes | 1.28+ |
| Helm | 3.12+ |
| Fleet hub datastore | Postgres (any recent version; `lib/pq` driver) |
| Container image | `linux/amd64`, `linux/arm64` — published to `ghcr.io/<org>/poddoctor` on tagged releases |

---

## License

Apache License 2.0
