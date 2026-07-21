package hub

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/chenar/poddoctor/internal/notify"
	"github.com/chenar/poddoctor/internal/severity"
	"github.com/chenar/poddoctor/internal/tracelink"
	"github.com/chenar/poddoctor/internal/webui"
)

// diagnosisStore is what Server needs from storage — satisfied by *Store,
// and by a fake in tests so handlers don't need a real Postgres.
type diagnosisStore interface {
	Insert(ctx context.Context, d Diagnosis) error
	List(ctx context.Context, f Filter) ([]Diagnosis, error)
	Ping(ctx context.Context) error
}

// Server is the fleet hub's HTTP API: an ingest endpoint clusters POST
// diagnoses to, a JSON list API, and the dashboard SPA over the same data.
type Server struct {
	store   diagnosisStore
	token   string
	tracing tracelink.Config
}

// NewServer builds a Server. An empty token disables auth on every
// endpoint — fine for local testing, not recommended once reachable from
// more than one trusted cluster's egress. A zero-value tracing Config
// just omits tracesURL from every record.
func NewServer(store diagnosisStore, token string, tracing tracelink.Config) *Server {
	return &Server{store: store, token: token, tracing: tracing}
}

// Routes returns the hub's http.Handler.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.Handle("/ingest", s.authenticated(http.HandlerFunc(s.handleIngest)))
	mux.Handle("/api/diagnoses", s.authenticated(http.HandlerFunc(s.handleAPIList)))
	mux.Handle("/", s.authenticated(webui.Handler()))
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
		Restarts:        p.Restarts,
		RolloutContext:  p.RolloutContext,
		LogExcerpt:      p.LogExcerpt,
		RecentEvents:    p.RecentEvents,
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

// apiRecord is the JSON shape the dashboard SPA consumes — Diagnosis plus
// a server-computed Severity, so the badge-color classification lives in
// exactly one place (internal/severity) rather than being duplicated in
// TypeScript.
type apiRecord struct {
	Diagnosis
	Severity  string `json:"severity"`
	TracesURL string `json:"tracesURL,omitempty"`
}

func (s *Server) handleAPIList(w http.ResponseWriter, r *http.Request) {
	diags, err := s.store.List(r.Context(), filterFromQuery(r.URL.Query()))
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}

	out := make([]apiRecord, 0, len(diags))
	for _, d := range diags {
		out = append(out, apiRecord{
			Diagnosis: d,
			Severity:  severity.Of(d.RootCause),
			TracesURL: s.tracing.URL(d.Namespace, d.Pod),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
