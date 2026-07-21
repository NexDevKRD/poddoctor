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
