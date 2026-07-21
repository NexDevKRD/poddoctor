## What

<!-- One or two sentences: what does this change do? -->

## Why

<!-- What problem does this solve, or what's the motivation? -->

## Testing

<!-- `task test`? `task demo` against a kind cluster? Manual kubectl steps? -->

## Checklist

- [ ] `task fmt lint test` pass
- [ ] Updated `charts/poddoctor` values/templates if this changes configuration
- [ ] Ran `task crd:diff` if `api/v1alpha1` changed
- [ ] Updated README/PRODUCTION.md/TESTING.md if user-facing behavior changed
