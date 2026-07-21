# Testing Guide

Complete testing reference for PodDoctor — from local unit tests to full end-to-end testing in a live cluster.

## Test Matrix

| Level | What it tests | Command | Requires cluster? |
|-------|--------------|---------|-------------------|
| Unit | Rule-based diagnosis engine | `task test` | No |
| Unit | Reconciler logic (fake client/clientset) | `task test` | No |
| E2E | Full operator, real crash loops | `task test:e2e` | Yes (kind/any cluster) |

There is no envtest/integration tier: the reconciler unit tests already use `controller-runtime`'s fake client plus `client-go`'s fake clientset, which covers the same reconcile-loop logic envtest would exercise, without needing a real `kube-apiserver` binary. The E2E tier is what catches anything envtest can't fake — real container crashes, real kubelet-reported reasons, real log streaming.

## Running Tests

### All Unit Tests

```bash
task test
```

Output:

```
=== RUN   TestDiagnose
=== RUN   TestDiagnose/image_pull_backoff
=== RUN   TestDiagnose/oom_killed_via_reason
=== RUN   TestDiagnose/exit_137_without_explicit_reason
=== RUN   TestDiagnose/probe_failure_overrides_sigterm
...
--- PASS: TestDiagnose (0.00s)
=== RUN   TestReconcile_DiagnosesOOMKilledCrashLoop
--- PASS: TestReconcile_DiagnosesOOMKilledCrashLoop (0.01s)
=== RUN   TestReconcile_SkipsHealthyPod
--- PASS: TestReconcile_SkipsHealthyPod (0.00s)
=== RUN   TestReconcile_DedupesSameEpisode
--- PASS: TestReconcile_DedupesSameEpisode (0.01s)
PASS
```

### With Coverage Report

```bash
go test ./... -coverprofile=cover.out -v
go tool cover -html=cover.out -o coverage.html
```

### Run Specific Test

```bash
go test ./internal/diagnosis/  -run TestDiagnose/oom_killed_via_reason -v
go test ./internal/controller/ -run TestReconcile_DedupesSameEpisode -v
```

## Test Coverage Details

### Diagnosis Engine Tests (`internal/diagnosis/rules_test.go`)

Pure function, no cluster/fake objects needed — a table test over `Evidence` → expected `RootCause`:

| Test case | Validates |
|-----------|-----------|
| `image pull backoff` / `err image pull` | `ImagePullBackOff`/`ErrImagePull` → `ImagePullError` |
| `oom killed via reason` | Kubelet-reported `OOMKilled` reason takes precedence |
| `exit 137 without explicit reason` | Exit code fallback still infers `OOMKilled` |
| `segfault` (exit 139) | → `SegFault` |
| `sigterm` (exit 143) | → `SignalKilled` |
| `bad command not executable` (exit 126) / `command not found` (exit 127) | → `BadCommand` |
| `generic application error` (exit 1) | → `ApplicationError` |
| `no signal at all` | No waiting/terminated state → `Unknown`, never empty summary |
| `unknown but recent rollout becomes the lead` | Rollout timing alone can drive the root cause when nothing else matches |
| `probe failure overrides sigterm` | A `Liveness probe failed` Event reclassifies a bare SIGTERM as `ProbeFailure` |

### Controller Tests (`internal/controller/poddiagnosis_controller_test.go`)

Uses `sigs.k8s.io/controller-runtime/pkg/client/fake` (with `WithStatusSubresource` so `Status().Update` behaves like the real API server) and `k8s.io/client-go/kubernetes/fake` for the clientset (logs/events):

| Test | Validates |
|------|-----------|
| `TestReconcile_DiagnosesOOMKilledCrashLoop` | End-to-end reconcile creates a `PodDiagnosis` with correct root cause, phase, restart count, and owner reference |
| `TestReconcile_SkipsHealthyPod` | A running, non-failing pod produces no `PodDiagnosis` and no requeue |
| `TestReconcile_DedupesSameEpisode` | A second reconcile at the same restart count doesn't reset `FirstObserved` (idempotent, cheap no-op) |

## E2E Testing with kind

`hack/e2e-test.sh` drives three *real* failure modes against an already-deployed operator — it doesn't mock anything, so it's the only tier that proves the kubelet-reported reasons, log streaming, and Event correlation actually work against a real API server:

```bash
# 1. Create a local cluster and load the image
kind create cluster --name poddoctor-test
docker build -t poddoctor:e2e .
kind load docker-image poddoctor:e2e --name poddoctor-test

# 2. Install via Helm
helm upgrade --install poddoctor charts/poddoctor \
  -n poddoctor-system --create-namespace \
  --set image.repository=poddoctor --set image.tag=e2e --set image.pullPolicy=IfNotPresent \
  --wait

# 3. Run the E2E suite
task test:e2e

# 4. Cleanup
kind delete cluster --name poddoctor-test
```

Or all of the above in one shot:

```bash
task demo        # kind cluster → build → helm install → real crash loops → results
task demo:clean
```

`hack/e2e-test.sh` covers:

1. `demo-oomkilled.yaml` → asserts `status.rootCause == OOMKilled`, non-empty `confidence`/`summary`.
2. `demo-imagepull-error.yaml` → asserts `status.rootCause == ImagePullError`.
3. `demo-bad-command.yaml` → asserts `status.rootCause == BadCommand`.
4. Owner reference: the `PodDiagnosis` is owned by the `Pod` (so it's garbage collected when the pod is deleted).
5. `kubectl get pd` short name and printer columns render correctly.

Each assertion polls (`wait_for_diagnosis`) rather than sleeping a fixed amount, since real image pulls / OOM kills / kubelet backoff timing vary across environments.

## CI Pipeline Testing

### GitHub Actions

```yaml
name: Test
on: [push, pull_request]
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
