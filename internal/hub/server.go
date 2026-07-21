package hub

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/chenar/poddoctor/internal/notify"
)

// diagnosisStore is what Server needs from storage — satisfied by *Store,
// and by a fake in tests so handlers don't need a real Postgres.
type diagnosisStore interface {
	Insert(ctx context.Context, d Diagnosis) error
	List(ctx context.Context, f Filter) ([]Diagnosis, error)
	Ping(ctx context.Context) error
}

// Server is the fleet hub's HTTP API: an ingest endpoint clusters POST
// diagnoses to, a JSON list API, and an HTML dashboard over the same data.
type Server struct {
	store diagnosisStore
	token string
}

// NewServer builds a Server. An empty token disables auth on every
// endpoint — fine for local testing, not recommended once reachable from
// more than one trusted cluster's egress.
func NewServer(store diagnosisStore, token string) *Server {
	return &Server{store: store, token: token}
}

// Routes returns the hub's http.Handler.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.Handle("/ingest", s.authenticated(http.HandlerFunc(s.handleIngest)))
	mux.Handle("/api/diagnoses", s.authenticated(http.HandlerFunc(s.handleAPIList)))
	mux.Handle("/", s.authenticated(http.HandlerFunc(s.handleDashboard)))
	return mux
}

func (s *Server) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		http.Error(w, "database not reachable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleIngest is what a cluster's PodDoctor --webhook-url points at: the
// same Payload it would otherwise POST to Slack, just stored instead.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var p notify.Payload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if p.Namespace == "" || p.Pod == "" {
		http.Error(w, "namespace and pod are required", http.StatusBadRequest)
		return
	}

	d := Diagnosis{
		Cluster:         p.Cluster,
		Namespace:       p.Namespace,
		Pod:             p.Pod,
		Container:       p.Container,
		RootCause:       p.RootCause,
		Confidence:      p.Confidence,
		Summary:         p.Summary,
		Recommendation:  p.Recommendation,
		SuppressedCount: p.SuppressedCount,
	}
	if err := s.store.Insert(r.Context(), d); err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func filterFromQuery(q url.Values) Filter {
	limit, _ := strconv.Atoi(q.Get("limit"))
	return Filter{
		Cluster:   q.Get("cluster"),
		Namespace: q.Get("namespace"),
		RootCause: q.Get("rootCause"),
		Limit:     limit,
	}
}

func (s *Server) handleAPIList(w http.ResponseWriter, r *http.Request) {
	diags, err := s.store.List(r.Context(), filterFromQuery(r.URL.Query()))
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(diags)
}

type dashboardRow struct {
	Cluster        string
	Namespace      string
	Pod            string
	Container      string
	RootCause      string
	Confidence     string
	Summary        string
	Recommendation string
	Suppressed     int
	ReceivedAgo    string
}

var dashboardPage = template.Must(template.New("hub-dashboard").Funcs(template.FuncMap{
	"causeClass": causeClass,
}).Parse(dashboardTemplate))

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	diags, err := s.store.List(r.Context(), filterFromQuery(q))
	if err != nil {
		http.Error(w, "storage error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	rows := make([]dashboardRow, 0, len(diags))
	for _, d := range diags {
		rows = append(rows, dashboardRow{
			Cluster:        d.Cluster,
			Namespace:      d.Namespace,
			Pod:            d.Pod,
			Container:      d.Container,
			RootCause:      d.RootCause,
			Confidence:     d.Confidence,
			Summary:        d.Summary,
			Recommendation: d.Recommendation,
			Suppressed:     d.SuppressedCount,
			ReceivedAgo:    duration.HumanDuration(time.Since(d.ReceivedAt)) + " ago",
		})
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = dashboardPage.Execute(w, struct {
		Rows      []dashboardRow
		Count     int
		Cluster   string
		Namespace string
		RootCause string
	}{Rows: rows, Count: len(rows), Cluster: q.Get("cluster"), Namespace: q.Get("namespace"), RootCause: q.Get("rootCause")})
}

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

const dashboardTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="30">
<title>PodDoctor Fleet Hub</title>
<style>
  :root { color-scheme: light dark; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; margin: 2rem; background: #0d1117; color: #c9d1d9; }
  h1 { font-size: 1.3rem; margin-bottom: .2rem; }
  .sub { color: #8b949e; margin-bottom: 1rem; font-size: .9rem; }
  form { margin-bottom: 1.5rem; display: flex; gap: .5rem; }
  input { background: #161b22; border: 1px solid #30363d; color: #c9d1d9; padding: .4rem .6rem; border-radius: 6px; font-size: .85rem; }
  button { background: #21262d; border: 1px solid #30363d; color: #c9d1d9; padding: .4rem .8rem; border-radius: 6px; cursor: pointer; }
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
  .cluster      { font-family: ui-monospace, monospace; font-size: .8rem; color: #58a6ff; }
  .pod          { font-family: ui-monospace, monospace; font-size: .85rem; }
  .container    { font-family: ui-monospace, monospace; font-size: .75rem; color: #8b949e; }
  .ns           { color: #8b949e; }
  .summary      { max-width: 30ch; }
  .rec          { max-width: 30ch; color: #8b949e; }
  .empty        { color: #8b949e; padding: 2rem 0; }
</style>
</head>
<body>
  <h1>PodDoctor Fleet Hub</h1>
  <div class="sub">{{ .Count }} recent diagnoses across all clusters &middot; refreshes every 30s</div>
  <form method="get">
    <input type="text" name="cluster" placeholder="cluster" value="{{ .Cluster }}">
    <input type="text" name="namespace" placeholder="namespace" value="{{ .Namespace }}">
    <input type="text" name="rootCause" placeholder="root cause" value="{{ .RootCause }}">
    <button type="submit">Filter</button>
  </form>
  {{ if eq .Count 0 }}
    <div class="empty">No diagnoses match this filter yet.</div>
  {{ else }}
  <table>
    <tr>
      <th>Cluster</th>
      <th>Pod</th>
      <th>Root Cause</th>
      <th>Summary</th>
      <th>Recommendation</th>
      <th>Received</th>
    </tr>
    {{ range .Rows }}
    <tr>
      <td><span class="cluster">{{ .Cluster }}</span></td>
      <td><div class="pod">{{ .Pod }}</div><div class="container">{{ .Container }}</div><div class="ns">{{ .Namespace }}</div></td>
      <td><span class="badge {{ causeClass .RootCause }}">{{ .RootCause }}</span><div class="conf">{{ .Confidence }} confidence</div></td>
      <td class="summary">{{ .Summary }}{{ if gt .Suppressed 0 }} <span class="conf">(+{{ .Suppressed }} more)</span>{{ end }}</td>
      <td class="rec">{{ .Recommendation }}</td>
      <td>{{ .ReceivedAgo }}</td>
    </tr>
    {{ end }}
  </table>
  {{ end }}
</body>
</html>
`
