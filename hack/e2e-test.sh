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
  assert_not_empty "demo-oomkilled has summary" "$SUMMARY"
else
  echo "FAIL: demo-oomkilled was never diagnosed within timeout"
  FAIL=$((FAIL+1))
fi
echo ""

echo "--- Test 2: bad image ref is diagnosed as ImagePullError ---"
kubectl apply -f config/samples/demo-imagepull-error.yaml
if wait_for_diagnosis demo-image-pull-error 60; then
  ROOTCAUSE=$(kubectl get pd demo-image-pull-error -o jsonpath='{.status.rootCause}')
  assert_eq "demo-image-pull-error root cause" "ImagePullError" "$ROOTCAUSE"
else
  echo "FAIL: demo-image-pull-error was never diagnosed within timeout"
  FAIL=$((FAIL+1))
fi
echo ""

echo "--- Test 3: bad command is diagnosed as BadCommand ---"
kubectl apply -f config/samples/demo-bad-command.yaml
if wait_for_diagnosis demo-bad-command 90; then
  ROOTCAUSE=$(kubectl get pd demo-bad-command -o jsonpath='{.status.rootCause}')
  assert_eq "demo-bad-command root cause" "BadCommand" "$ROOTCAUSE"
else
  echo "FAIL: demo-bad-command was never diagnosed within timeout"
  FAIL=$((FAIL+1))
fi
echo ""

echo "--- Test 4: PodDiagnosis is owned by the Pod (garbage collected with it) ---"
OWNER_KIND=$(kubectl get pd demo-oomkilled -o jsonpath='{.metadata.ownerReferences[0].kind}')
assert_eq "demo-oomkilled owned by Pod" "Pod" "$OWNER_KIND"
echo ""

echo "--- Test 5: kubectl short name and printer columns work ---"
kubectl get pd > /dev/null 2>&1
assert_eq "kubectl get pd works" "0" "$?"

OUTPUT=$(kubectl get pd demo-oomkilled --no-headers)
echo "$OUTPUT" | grep -q "OOMKilled" && echo "PASS: Root Cause column visible" && PASS=$((PASS+1)) || { echo "FAIL: Root Cause column not visible"; FAIL=$((FAIL+1)); }
echo ""

echo "--- Cleanup ---"
kubectl delete -f config/samples/demo-oomkilled.yaml --ignore-not-found
kubectl delete -f config/samples/demo-imagepull-error.yaml --ignore-not-found
kubectl delete -f config/samples/demo-bad-command.yaml --ignore-not-found
echo ""

echo "=== Results ==="
echo "PASS: $PASS"
echo "FAIL: $FAIL"
echo ""

if [ "$FAIL" -gt 0 ]; then
  echo "E2E TESTS FAILED"
  exit 1
fi

echo "ALL E2E TESTS PASSED"
