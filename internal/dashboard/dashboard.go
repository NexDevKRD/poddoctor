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
