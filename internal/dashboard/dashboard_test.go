package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	diagv1alpha1 "github.com/chenar/poddoctor/api/v1alpha1"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := diagv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func TestAPIList_EmptyState(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()

	rec := httptest.NewRecorder()
	Handler(c).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/diagnoses", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []apiRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %+v", got)
	}
}

func TestAPIList_ReturnsDiagnoses(t *testing.T) {
	diag := &diagv1alpha1.PodDiagnosis{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-oomkilled-app", Namespace: "default"},
		Spec:       diagv1alpha1.PodDiagnosisSpec{PodName: "demo-oomkilled", PodNamespace: "default", ContainerName: "app"},
		Status: diagv1alpha1.PodDiagnosisStatus{
			Phase:          "Diagnosed",
			RootCause:      diagv1alpha1.RootCauseOOMKilled,
			Confidence:     diagv1alpha1.ConfidenceHigh,
			Summary:        "Container exceeded its memory limit and was killed by the kernel OOM killer.",
			Recommendation: "Raise resources.limits.memory.",
			RestartCount:   3,
			RolloutContext: "started 45s after deployment/api rolled to revision 12",
			LogExcerpt:     "panic: out of memory",
			RecentEvents: []diagv1alpha1.EvidenceEvent{
				{Reason: "BackOff", Message: "Back-off restarting failed container", Count: 3},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(diag).Build()

	rec := httptest.NewRecorder()
	Handler(c).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/diagnoses", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []apiRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 record, got %d", len(got))
	}
	r := got[0]
	if r.Pod != "demo-oomkilled" || r.Container != "app" || r.RootCause != "OOMKilled" || r.Severity != "critical" {
		t.Fatalf("unexpected record: %+v", r)
	}
	if r.RolloutContext == "" || r.LogExcerpt == "" || len(r.RecentEvents) != 1 {
		t.Fatalf("expected evidence fields populated, got: %+v", r)
	}
}

func TestHandler_ServesStaticAssetsAtRoot(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()

	rec := httptest.NewRecorder()
	Handler(c).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
