# PodDoctor dashboard

React + Vite + TypeScript SPA, embedded via `go:embed` into both
`cmd/main.go` (per-cluster dashboard) and `cmd/hub` (fleet hub). Same
frontend, driven entirely by whichever `/api/diagnoses` JSON endpoint it's
served alongside — see `src/types.ts` for the shared response shape.

## Build

`npm run build` outputs straight into `../internal/webui/dist` (see
`vite.config.ts`) — that's what `go build` embeds. From the repo root,
`task web:build` does the same and is a dependency of `task build`/`test`/`lint`.

## Local iteration

`npm run dev` for hot reload. It has no backend of its own; either point
`fetch('api/diagnoses')` at a running operator/hub during dev, or hand-edit
fixture data into `useDiagnoses.ts` temporarily while working on layout.

## Structure

- `src/types.ts` — the `Diagnosis` shape both APIs return.
- `src/useDiagnoses.ts` — polls `/api/diagnoses` every 5s, no full-page reload.
- `src/App.tsx` — table, search/filter, expandable evidence detail, severity summary.
- `src/relativeTime.ts` — client-side "3m ago" formatting.
