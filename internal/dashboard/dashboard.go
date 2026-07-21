// Package dashboard serves the dashboard SPA (see web/, embedded via
// internal/webui) plus the JSON API it reads from — a human-friendly view
// over `kubectl get pd -A` for people who don't want a terminal open.
package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	diagv1alpha1 "github.com/chenar/poddoctor/api/v1alpha1"
	"github.com/chenar/poddoctor/internal/severity"
	"github.com/chenar/poddoctor/internal/webui"
)

// apiRecord is the JSON shape the dashboard SPA consumes. Shared in spirit
// (not in code, different packages/modules of data) with the fleet hub's
// apiRecord — same field names, so the same frontend renders either.
type apiRecord struct {
	Namespace      string   `json:"namespace"`
	Pod            string   `json:"pod"`
	Container      string   `json:"container"`
	RootCause      string   `json:"rootCause"`
	Severity       string   `json:"severity"`
	Confidence     string   `json:"confidence"`
	Restarts       int32    `json:"restarts"`
	Summary        string   `json:"summary"`
	Recommendation string   `json:"recommendation"`
	RolloutContext string   `json:"rolloutContext,omitempty"`
	LogExcerpt     string   `json:"logExcerpt,omitempty"`
	RecentEvents   []string `json:"recentEvents,omitempty"`
	LastObserved   string   `json:"lastObserved"`
}

// Handler returns the dashboard's http.Handler: the SPA's static assets at
// "/" and its data at "/api/diagnoses", read through reader. Pass
// mgr.GetAPIReader() so the page works immediately (no wait for the
// manager's cache to sync).
func Handler(reader client.Reader) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/diagnoses", handleAPIList(reader))
	mux.Handle("/", webui.Handler())
	return mux
}

func handleAPIList(reader client.Reader) http.HandlerFunc {
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

		out := make([]apiRecord, 0, len(items))
		for _, d := range items {
			events := make([]string, 0, len(d.Status.RecentEvents))
			for _, e := range d.Status.RecentEvents {
				events = append(events, fmt.Sprintf("%s: %s (x%d)", e.Reason, e.Message, e.Count))
			}

			var lastObserved string
			if !d.Status.LastObserved.IsZero() {
				lastObserved = d.Status.LastObserved.Format(time.RFC3339)
			}

			out = append(out, apiRecord{
				Namespace:      d.Namespace,
				Pod:            d.Spec.PodName,
				Container:      d.Spec.ContainerName,
				RootCause:      string(d.Status.RootCause),
				Severity:       severity.Of(string(d.Status.RootCause)),
				Confidence:     string(d.Status.Confidence),
				Restarts:       d.Status.RestartCount,
				Summary:        d.Status.Summary,
				Recommendation: d.Status.Recommendation,
				RolloutContext: d.Status.RolloutContext,
				LogExcerpt:     d.Status.LogExcerpt,
				RecentEvents:   events,
				LastObserved:   lastObserved,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}
}
