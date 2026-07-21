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

## Step 1: Build & Push Container Image

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

By default PodDoctor watches Pods cluster-wide (needed for most real deployments — crashes happen in app namespaces, not just its own). To restrict it to one namespace instead:

```bash
helm upgrade poddoctor charts/poddoctor -n poddoctor-system --reuse-values \
  --set watchNamespace=my-app-namespace
```

Note: the ClusterRole is unchanged either way (cluster-wide read access is still granted) — this only restricts what the controller's cache watches, not what it's permitted to see. If you need namespace-scoped RBAC too, swap the Helm chart's ClusterRole/ClusterRoleBinding for a Role/RoleBinding in that namespace manually.

## Step 6: Network Policy (Zero-Trust)

The operator only needs to reach the Kubernetes API server (for watches, log fetches, and event reads) — no other egress:

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
