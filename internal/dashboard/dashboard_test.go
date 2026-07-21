package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestHandler_EmptyState(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()

	rec := httptest.NewRecorder()
	Handler(c)(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No crash loops diagnosed") {
		t.Fatalf("expected empty-state message, got: %s", rec.Body.String())
	}
}

func TestHandler_ListsDiagnoses(t *testing.T) {
	diag := &diagv1alpha1.PodDiagnosis{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-oomkilled", Namespace: "default"},
		Spec:       diagv1alpha1.PodDiagnosisSpec{PodName: "demo-oomkilled", PodNamespace: "default", ContainerName: "app"},
		Status: diagv1alpha1.PodDiagnosisStatus{
			Phase:          "Diagnosed",
			RootCause:      diagv1alpha1.RootCauseOOMKilled,
			Confidence:     diagv1alpha1.ConfidenceHigh,
			Summary:        "Container exceeded its memory limit and was killed by the kernel OOM killer.",
			Recommendation: "Raise resources.limits.memory.",
			RestartCount:   3,
		},
	}
	c := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(diag).Build()

	rec := httptest.NewRecorder()
	Handler(c)(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	for _, want := range []string{"demo-oomkilled", "OOMKilled", "High confidence", "Raise resources.limits.memory."} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected body to contain %q, got:\n%s", want, body)
		}
	}
}

func TestCauseClass(t *testing.T) {
	cases := map[string]string{
		"OOMKilled":      "sev-critical",
		"ImagePullError": "sev-high",
		"SignalKilled":   "sev-medium",
		"Unknown":        "sev-unknown",
	}
	for cause, want := range cases {
		if got := causeClass(cause); got != want {
			t.Errorf("causeClass(%q) = %q, want %q", cause, got, want)
		}
	}
}
