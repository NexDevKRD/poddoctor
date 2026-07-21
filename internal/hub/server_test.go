package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chenar/poddoctor/internal/notify"
)

type fakeStore struct {
	inserted   []Diagnosis
	listResult []Diagnosis
	pingErr    error
}

func (f *fakeStore) Insert(_ context.Context, d Diagnosis) error {
	f.inserted = append(f.inserted, d)
	return nil
}

func (f *fakeStore) List(_ context.Context, _ Filter) ([]Diagnosis, error) {
	return f.listResult, nil
}

func (f *fakeStore) Ping(_ context.Context) error { return f.pingErr }

func TestIngest_StoresPayload(t *testing.T) {
	fs := &fakeStore{}
	srv := NewServer(fs, "")

	body, _ := json.Marshal(notify.Payload{
		Cluster: "us-east-1", Namespace: "payments", Pod: "api-1", Container: "app",
		RootCause: "OOMKilled", Confidence: "High", SuppressedCount: 3,
	})
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusAccepted, w.Body.String())
	}
	if len(fs.inserted) != 1 {
		t.Fatalf("expected 1 insert, got %d", len(fs.inserted))
	}
	got := fs.inserted[0]
	if got.Cluster != "us-east-1" || got.Pod != "api-1" || got.SuppressedCount != 3 {
		t.Fatalf("unexpected inserted diagnosis: %+v", got)
	}
}

func TestIngest_RejectsMissingFields(t *testing.T) {
	fs := &fakeStore{}
	srv := NewServer(fs, "")

	body, _ := json.Marshal(notify.Payload{Cluster: "us-east-1"}) // no namespace/pod
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if len(fs.inserted) != 0 {
		t.Fatalf("expected no insert for invalid payload")
	}
}

func TestIngest_RejectsGet(t *testing.T) {
	srv := NewServer(&fakeStore{}, "")
	req := httptest.NewRequest(http.MethodGet, "/ingest", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestAuth_RequiresBearerTokenWhenSet(t *testing.T) {
	srv := NewServer(&fakeStore{}, "s3cr3t")

	req := httptest.NewRequest(http.MethodGet, "/api/diagnoses", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want %d", w.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/diagnoses", nil)
	req.Header.Set("Authorization", "Bearer s3cr3t")
	w = httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("correct token: status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestAuth_DisabledWhenTokenEmpty(t *testing.T) {
	srv := NewServer(&fakeStore{}, "")
	req := httptest.NewRequest(http.MethodGet, "/api/diagnoses", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestAPIList_ReturnsJSON(t *testing.T) {
	fs := &fakeStore{listResult: []Diagnosis{
		{Cluster: "us-east-1", Namespace: "payments", Pod: "api-1", RootCause: "OOMKilled"},
	}}
	srv := NewServer(fs, "")

	req := httptest.NewRequest(http.MethodGet, "/api/diagnoses?cluster=us-east-1", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	var got []Diagnosis
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 || got[0].Pod != "api-1" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestDashboard_RendersHTML(t *testing.T) {
	fs := &fakeStore{listResult: []Diagnosis{
		{Cluster: "us-east-1", Namespace: "payments", Pod: "api-1", RootCause: "OOMKilled", Confidence: "High", Summary: "OOM"},
	}}
	srv := NewServer(fs, "")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if body := w.Body.String(); !bytes.Contains([]byte(body), []byte("api-1")) {
		t.Fatalf("expected dashboard body to mention pod name, got: %s", body)
	}
}

func TestHealthzReadyz(t *testing.T) {
	srv := NewServer(&fakeStore{}, "")

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", w.Code)
	}

	w = httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("readyz status = %d", w.Code)
	}
}

func TestReadyz_FailsWhenDBUnreachable(t *testing.T) {
	srv := NewServer(&fakeStore{pingErr: context.DeadlineExceeded}, "")
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}
