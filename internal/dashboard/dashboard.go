// Package dashboard serves a small read-only HTML page listing every
// PodDiagnosis in the cluster, newest first — a human-friendly view over
// `kubectl get pd -A` for people who don't want a terminal open.
package dashboard

import (
	"html/template"
	"net/http"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/util/duration"
	"sigs.k8s.io/controller-runtime/pkg/client"

	diagv1alpha1 "github.com/chenar/poddoctor/api/v1alpha1"
)

type row struct {
	Namespace      string
	Pod            string
	RootCause      string
	Confidence     string
	Restarts       int32
	Summary        string
	Recommendation string
	LastObserved   string
}

var page = template.Must(template.New("dashboard").Funcs(template.FuncMap{
	"causeClass": causeClass,
}).Parse(pageTemplate))

// Handler returns an http.HandlerFunc that renders the dashboard by reading
// PodDiagnosis objects through reader. Pass mgr.GetAPIReader() so the page
// works immediately (no wait for the manager's cache to sync).
func Handler(reader client.Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var list diagv1alpha1.PodDiagnosisList
		if err := reader.List(r.Context(), &list); err != nil {
			http.Error(w, "failed to list PodDiagnosis: "+err.Error(), http.StatusInternalServerError)
			return
		}

		items := list.Items
		sort.Slice(items, func(i, j int) bool {
			return items[j].Status.LastObserved.Before(&items[i].Status.LastObserved)
		})

		rows := make([]row, 0, len(items))
		for _, d := range items {
			rows = append(rows, row{
				Namespace:      d.Namespace,
				Pod:            d.Spec.PodName,
				RootCause:      string(d.Status.RootCause),
				Confidence:     string(d.Status.Confidence),
				Restarts:       d.Status.RestartCount,
				Summary:        d.Status.Summary,
				Recommendation: d.Status.Recommendation,
				LastObserved:   humanAgo(d.Status.LastObserved.Time),
			})
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = page.Execute(w, struct {
			Rows  []row
			Count int
		}{Rows: rows, Count: len(rows)})
	}
}

func humanAgo(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return duration.HumanDuration(time.Since(t)) + " ago"
}

// causeClass maps a root cause to a CSS class for its badge color.
func causeClass(rootCause string) string {
	switch rootCause {
	case "OOMKilled", "SegFault":
		return "sev-critical"
	case "ImagePullError", "BadCommand", "ProbeFailure":
		return "sev-high"
	case "SignalKilled", "RecentRolloutRegression", "ApplicationError":
		return "sev-medium"
	default:
		return "sev-unknown"
	}
}

const pageTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="15">
<title>PodDoctor</title>
<style>
  :root { color-scheme: light dark; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; margin: 2rem; background: #0d1117; color: #c9d1d9; }
  h1 { font-size: 1.3rem; margin-bottom: .2rem; }
  .sub { color: #8b949e; margin-bottom: 1.5rem; font-size: .9rem; }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: .6rem .8rem; border-bottom: 1px solid #21262d; vertical-align: top; }
  th { color: #8b949e; font-weight: 600; font-size: .8rem; text-transform: uppercase; letter-spacing: .03em; }
  tr:hover { background: #161b22; }
  .badge { display: inline-block; padding: .15rem .55rem; border-radius: 1rem; font-size: .8rem; font-weight: 600; }
  .sev-critical { background: #3d1b1e; color: #f85149; }
  .sev-high     { background: #3d2c1b; color: #e3b341; }
  .sev-medium   { background: #1b2a3d; color: #58a6ff; }
  .sev-unknown  { background: #21262d; color: #8b949e; }
  .conf         { color: #8b949e; font-size: .8rem; }
  .pod { font-family: ui-monospace, monospace; font-size: .85rem; }
  .ns  { color: #8b949e; }
  .summary { max-width: 30ch; }
  .rec { max-width: 30ch; color: #8b949e; }
  .empty { color: #8b949e; padding: 2rem 0; }
</style>
</head>
<body>
  <h1>PodDoctor</h1>
  <div class="sub">{{ .Count }} diagnosed failure{{ if ne .Count 1 }}s{{ end }} &middot; refreshes every 15s</div>
  {{ if eq .Count 0 }}
    <div class="empty">No crash loops diagnosed. Either everything's healthy, or nothing's crashed yet.</div>
  {{ else }}
  <table>
    <tr>
      <th>Pod</th>
      <th>Root Cause</th>
      <th>Restarts</th>
      <th>Summary</th>
      <th>Recommendation</th>
      <th>Last Seen</th>
    </tr>
    {{ range .Rows }}
    <tr>
      <td><div class="pod">{{ .Pod }}</div><div class="ns">{{ .Namespace }}</div></td>
      <td><span class="badge {{ causeClass .RootCause }}">{{ .RootCause }}</span><div class="conf">{{ .Confidence }} confidence</div></td>
      <td>{{ .Restarts }}</td>
      <td class="summary">{{ .Summary }}</td>
      <td class="rec">{{ .Recommendation }}</td>
      <td>{{ .LastObserved }}</td>
    </tr>
    {{ end }}
  </table>
  {{ end }}
</body>
</html>
`
