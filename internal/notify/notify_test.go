package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	diagv1alpha1 "github.com/chenar/poddoctor/api/v1alpha1"
)

func testDiag() *diagv1alpha1.PodDiagnosis {
	return &diagv1alpha1.PodDiagnosis{
		Spec: diagv1alpha1.PodDiagnosisSpec{
			PodName:       "payments-api-abc123",
			PodNamespace:  "default",
			ContainerName: "app",
		},
		Status: diagv1alpha1.PodDiagnosisStatus{
			RootCause:      diagv1alpha1.RootCauseOOMKilled,
			Confidence:     diagv1alpha1.ConfidenceHigh,
			Summary:        "Container exceeded its memory limit.",
			Recommendation: "Raise resources.limits.memory.",
		},
	}
}

func TestSend_Generic(t *testing.T) {
	var got Payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := Send(context.Background(), srv.URL, FormatGeneric, testDiag()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if got.Pod != "payments-api-abc123" || got.RootCause != "OOMKilled" {
		t.Fatalf("unexpected payload: %+v", got)
	}
}

func TestSend_Slack(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := Send(context.Background(), srv.URL, FormatSlack, testDiag()); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if got["text"] == "" {
		t.Fatalf("expected non-empty slack text field, got %+v", got)
	}
}

func TestSend_NonOKStatusIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if err := Send(context.Background(), srv.URL, FormatGeneric, testDiag()); err == nil {
		t.Fatal("expected error on non-2xx response, got nil")
	}
}
