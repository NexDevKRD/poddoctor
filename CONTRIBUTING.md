# Contributing to PodDoctor

## Development setup

```bash
git clone https://github.com/chenar/poddoctor.git
cd poddoctor
task test        # unit tests
task lint        # golangci-lint
task fmt         # gofmt + go vet
```

Full local workflow (kind cluster, build, deploy, trigger real failures) is `task demo` — see [`README.md`](README.md#development) and [`TESTING.md`](TESTING.md).

## Before opening a PR

- `task fmt lint test` all pass.
- New behavior in `internal/diagnosis` or `internal/controller` has a table-driven test alongside it (see existing `_test.go` files for the pattern).
- If you changed `api/v1alpha1/poddiagnosis_types.go`, regenerate deepcopy code: `task generate`, and keep `config/crd/bases/*.yaml` and `charts/poddoctor/templates/crd.yaml` in sync (`task crd:diff` checks this in CI).
- Keep PRs scoped to one change; unrelated cleanup makes review harder.

## Reporting bugs / requesting features

Use the issue templates. For a bug report, the most useful thing you can attach is the output of:

```bash
kubectl get pd -A
kubectl describe pd <name> -n <namespace>
kubectl logs -n poddoctor-system deployment/poddoctor
```

## Security issues

Do not open a public issue — see [`SECURITY.md`](SECURITY.md).
