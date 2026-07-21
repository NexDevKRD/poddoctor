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
| `RecentRolloutRegression` | Pod started within the rollout-correlation window of a new ReplicaSet | Medium |
| `ApplicationError` | Any other non-zero exit | Medium |
| `Unknown` | No matching signature | Low |

Rollout timing is layered onto whichever primary cause is found (e.g. "OOMKilled — and also started right after a rollout"), or becomes the lead cause itself when nothing else matches.

---

## Custom Resource

`PodDiagnosis` objects are **created and owned by the operator** — one per crash-looping pod, named after the pod, garbage-collected automatically when the pod is deleted (via `ownerReferences`). You don't write these; you read them.

```yaml
apiVersion: diagnostics.poddoctor.dev/v1alpha1
kind: PodDiagnosis
metadata:
  name: payments-api-7d4b8f6c9-x2k4p
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
NAMESPACE   NAME                             POD                              ROOT CAUSE      CONFIDENCE   RESTARTS   AGE
platform    payments-api-7d4b8f6c9-x2k4p     payments-api-7d4b8f6c9-x2k4p     OOMKilled       High         4          12m
```

