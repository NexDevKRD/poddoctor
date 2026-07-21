// Package dashboard serves a small read-only HTML page listing every
// PodDiagnosis in the cluster, newest first — a human-friendly view over
// `kubectl get pd -A` for people who don't want a terminal open.
package dashboard

import (
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/duration"
	"sigs.k8s.io/controller-runtime/pkg/client"

	diagv1alpha1 "github.com/chenar/poddoctor/api/v1alpha1"
)

type row struct {
	Namespace      string
	Pod            string
	Container      string
	RootCause      string
	Confidence     string
	Restarts       int32
	Summary        string
	Recommendation string
	RolloutContext string
	LogExcerpt     string
	RecentEvents   []string
	LastObserved   string
	// Search is a lowercased blob of the searchable fields, used by the
	// page's client-side filter box.
	Search string
}

// counts tallies rows by severity class, for the summary badges.
type counts struct {
	Critical, High, Medium, Unknown int
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

		var c counts
		rows := make([]row, 0, len(items))
		for _, d := range items {
			class := causeClass(string(d.Status.RootCause))
			switch class {
			case "sev-critical":
				c.Critical++
			case "sev-high":
				c.High++
			case "sev-medium":
				c.Medium++
			default:
				c.Unknown++
			}

			events := make([]string, 0, len(d.Status.RecentEvents))
			for _, e := range d.Status.RecentEvents {
				events = append(events, fmt.Sprintf("%s: %s (x%d)", e.Reason, e.Message, e.Count))
			}

			rows = append(rows, row{
				Namespace:      d.Namespace,
				Pod:            d.Spec.PodName,
				Container:      d.Spec.ContainerName,
				RootCause:      string(d.Status.RootCause),
				Confidence:     string(d.Status.Confidence),
				Restarts:       d.Status.RestartCount,
				Summary:        d.Status.Summary,
				Recommendation: d.Status.Recommendation,
				RolloutContext: d.Status.RolloutContext,
				LogExcerpt:     d.Status.LogExcerpt,
				RecentEvents:   events,
				LastObserved:   humanAgo(d.Status.LastObserved.Time),
				Search: strings.ToLower(strings.Join([]string{
					d.Namespace, d.Spec.PodName, d.Spec.ContainerName,
					string(d.Status.RootCause), d.Status.Summary,
				}, " ")),
			})
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = page.Execute(w, struct {
			Rows   []row
			Count  int
			Counts counts
		}{Rows: rows, Count: len(rows), Counts: c})
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
  .sub { color: #8b949e; margin-bottom: 1rem; font-size: .9rem; }
  .summary-badges { margin-bottom: 1rem; display: flex; gap: .5rem; flex-wrap: wrap; }
  .search-box { margin-bottom: 1rem; }
  .search-box input {
    width: 100%; max-width: 32rem; background: #161b22; border: 1px solid #30363d;
    color: #c9d1d9; padding: .5rem .8rem; border-radius: 6px; font-size: .9rem;
  }
  table { width: 100%; border-collapse: collapse; }
  th, td { text-align: left; padding: .6rem .8rem; border-bottom: 1px solid #21262d; vertical-align: top; }
  th { color: #8b949e; font-weight: 600; font-size: .8rem; text-transform: uppercase; letter-spacing: .03em; }
  tr.row { cursor: pointer; }
  tr.row:hover { background: #161b22; }
  tr.detail { display: none; background: #0a0d12; }
  tr.detail.open { display: table-row; }
  tr.detail td { padding: 1rem 1.5rem; }
  tr.detail h4 { margin: 0 0 .3rem; font-size: .75rem; text-transform: uppercase; letter-spacing: .03em; color: #8b949e; }
  tr.detail pre { white-space: pre-wrap; word-break: break-word; background: #010409; padding: .6rem; border-radius: 6px; font-size: .8rem; margin: 0 0 .8rem; max-height: 16rem; overflow-y: auto; }
  tr.detail ul { margin: 0 0 .8rem; padding-left: 1.2rem; font-size: .85rem; }
  .badge { display: inline-block; padding: .15rem .55rem; border-radius: 1rem; font-size: .8rem; font-weight: 600; }
  .sev-critical { background: #3d1b1e; color: #f85149; }
  .sev-high     { background: #3d2c1b; color: #e3b341; }
  .sev-medium   { background: #1b2a3d; color: #58a6ff; }
  .sev-unknown  { background: #21262d; color: #8b949e; }
  .conf         { color: #8b949e; font-size: .8rem; }
  .pod { font-family: ui-monospace, monospace; font-size: .85rem; }
  .container { font-family: ui-monospace, monospace; font-size: .75rem; color: #8b949e; }
  .ns  { color: #8b949e; }
  .summary { max-width: 30ch; }
  .rec { max-width: 30ch; color: #8b949e; }
  .empty { color: #8b949e; padding: 2rem 0; }
  .hint { color: #8b949e; font-size: .75rem; }
</style>
</head>
<body>
  <h1>PodDoctor</h1>
  <div class="sub">{{ .Count }} diagnosed failure{{ if ne .Count 1 }}s{{ end }} &middot; refreshes every 15s</div>
  {{ if gt .Count 0 }}
  <div class="summary-badges">
    {{ if gt .Counts.Critical 0 }}<span class="badge sev-critical">{{ .Counts.Critical }} critical</span>{{ end }}
    {{ if gt .Counts.High 0 }}<span class="badge sev-high">{{ .Counts.High }} high</span>{{ end }}
    {{ if gt .Counts.Medium 0 }}<span class="badge sev-medium">{{ .Counts.Medium }} medium</span>{{ end }}
    {{ if gt .Counts.Unknown 0 }}<span class="badge sev-unknown">{{ .Counts.Unknown }} unknown</span>{{ end }}
  </div>
  <div class="search-box">
    <input type="text" id="filter" placeholder="Filter by pod, namespace, container, or root cause..." autocomplete="off">
  </div>
  {{ end }}
  {{ if eq .Count 0 }}
    <div class="empty">No crash loops diagnosed. Either everything's healthy, or nothing's crashed yet.</div>
  {{ else }}
  <table id="diagTable">
    <tr>
      <th>Pod</th>
      <th>Root Cause</th>
      <th>Restarts</th>
      <th>Summary</th>
      <th>Recommendation</th>
      <th>Last Seen</th>
    </tr>
    {{ range $i, $row := .Rows }}
    <tr class="row" data-search="{{ $row.Search }}" onclick="toggleDetail({{ $i }})">
      <td><div class="pod">{{ $row.Pod }}</div><div class="container">{{ $row.Container }}</div><div class="ns">{{ $row.Namespace }}</div></td>
      <td><span class="badge {{ causeClass $row.RootCause }}">{{ $row.RootCause }}</span><div class="conf">{{ $row.Confidence }} confidence</div></td>
      <td>{{ $row.Restarts }}</td>
      <td class="summary">{{ $row.Summary }}</td>
      <td class="rec">{{ $row.Recommendation }}</td>
      <td>{{ $row.LastObserved }}</td>
    </tr>
    <tr class="detail" id="detail-{{ $i }}" data-search="{{ $row.Search }}">
      <td colspan="6">
        {{ if $row.RolloutContext }}<h4>Rollout Context</h4><div class="hint">{{ $row.RolloutContext }}</div>{{ end }}
        {{ if $row.RecentEvents }}
        <h4>Recent Events</h4>
        <ul>{{ range $row.RecentEvents }}<li>{{ . }}</li>{{ end }}</ul>
        {{ end }}
        {{ if $row.LogExcerpt }}
        <h4>Log Excerpt (previous instance)</h4>
        <pre>{{ $row.LogExcerpt }}</pre>
        {{ else }}
        <div class="hint">No previous-instance log excerpt available.</div>
        {{ end }}
      </td>
    </tr>
    {{ end }}
  </table>
  {{ end }}
<script>
function toggleDetail(i) {
  var d = document.getElementById('detail-' + i);
  if (d) d.classList.toggle('open');
}
var filterInput = document.getElementById('filter');
if (filterInput) {
  filterInput.addEventListener('input', function () {
    var q = this.value.toLowerCase().trim();
    document.querySelectorAll('#diagTable tr.row, #diagTable tr.detail').forEach(function (tr) {
      var match = !q || (tr.dataset.search || '').indexOf(q) !== -1;
      if (tr.classList.contains('row')) {
        tr.style.display = match ? '' : 'none';
      } else if (!match) {
        tr.classList.remove('open');
      }
    });
  });
}
</script>
</body>
</html>
`
