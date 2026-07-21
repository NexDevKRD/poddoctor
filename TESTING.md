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
