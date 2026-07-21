#!/usr/bin/env bash
set -euo pipefail

echo "=== PodDoctor E2E Tests ==="
echo ""

PASS=0
FAIL=0

assert_eq() {
  local desc="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then
    echo "PASS: $desc"
    PASS=$((PASS+1))
  else
    echo "FAIL: $desc (expected=$expected, got=$actual)"
    FAIL=$((FAIL+1))
  fi
}

assert_not_empty() {
  local desc="$1" actual="$2"
  if [ -n "$actual" ]; then
    echo "PASS: $desc"
    PASS=$((PASS+1))
  else
    echo "FAIL: $desc (value is empty)"
    FAIL=$((FAIL+1))
  fi
}

wait_for_diagnosis() {
  local name="$1" timeout="${2:-90}" waited=0
  while [ "$waited" -lt "$timeout" ]; do
    if [ "$(kubectl get pd "$name" -o jsonpath='{.status.phase}' 2>/dev/null)" = "Diagnosed" ]; then
      return 0
    fi
    sleep 3
    waited=$((waited + 3))
  done
  return 1
}

echo "--- Checking prerequisites ---"
kubectl cluster-info > /dev/null 2>&1 || { echo "ERROR: no cluster available"; exit 1; }
kubectl get crd poddiagnoses.diagnostics.poddoctor.dev > /dev/null 2>&1 || { echo "ERROR: CRD not installed. Run: task helm:install (or task install)"; exit 1; }
echo "OK: cluster and CRD available"
echo ""

echo "--- Test 1: OOMKilled pod is diagnosed as OOMKilled ---"
kubectl apply -f config/samples/demo-oomkilled.yaml
if wait_for_diagnosis demo-oomkilled 120; then
  ROOTCAUSE=$(kubectl get pd demo-oomkilled -o jsonpath='{.status.rootCause}')
  assert_eq "demo-oomkilled root cause" "OOMKilled" "$ROOTCAUSE"

  CONFIDENCE=$(kubectl get pd demo-oomkilled -o jsonpath='{.status.confidence}')
  assert_not_empty "demo-oomkilled has confidence" "$CONFIDENCE"

  SUMMARY=$(kubectl get pd demo-oomkilled -o jsonpath='{.status.summary}')
