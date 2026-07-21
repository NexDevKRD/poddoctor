package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	diagv1alpha1 "github.com/chenar/poddoctor/api/v1alpha1"
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("adding client-go scheme: %v", err)
	}
	if err := diagv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding diagv1alpha1 scheme: %v", err)
	}
	return scheme
}

func oomKilledPod() *corev1.Pod {
	one := int32(1)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "payments-api-abc123", Namespace: "default", UID: types.UID("pod-uid-1"),
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "app",
					RestartCount: one,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off restarting failed container"},
					},
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
					},
				},
			},
		},
	}
}

func newReconciler(t *testing.T, scheme *runtime.Scheme, initObjs ...client.Object) *PodDiagnosisReconciler {
	t.Helper()
	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&diagv1alpha1.PodDiagnosis{}).
		WithObjects(initObjs...)
	c := builder.Build()

	return &PodDiagnosisReconciler{
		Client:        c,
		Scheme:        scheme,
		ClientSet:     fakeclientset.NewSimpleClientset(),
		Recorder:      events.NewFakeRecorder(10),
		LogTailLines:  50,
		RolloutWindow: 10 * time.Minute,
	}
}

func TestReconcile_DiagnosesOOMKilledCrashLoop(t *testing.T) {
	scheme := newTestScheme(t)
	pod := oomKilledPod()
	r := newReconciler(t, scheme, pod)

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)}

	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.RequeueAfter != diagnosisRequeueInterval {
		t.Fatalf("expected requeue after %v, got %v", diagnosisRequeueInterval, res.RequeueAfter)
	}

	var diag diagv1alpha1.PodDiagnosis
	if err := r.Get(ctx, client.ObjectKeyFromObject(pod), &diag); err != nil {
		t.Fatalf("expected PodDiagnosis to be created: %v", err)
	}
	if diag.Status.RootCause != diagv1alpha1.RootCauseOOMKilled {
		t.Fatalf("RootCause = %s, want %s", diag.Status.RootCause, diagv1alpha1.RootCauseOOMKilled)
	}
	if diag.Status.Phase != "Diagnosed" {
		t.Fatalf("Phase = %s, want Diagnosed", diag.Status.Phase)
	}
	if diag.Status.RestartCount != 1 {
		t.Fatalf("RestartCount = %d, want 1", diag.Status.RestartCount)
	}
	if diag.OwnerReferences == nil || len(diag.OwnerReferences) != 1 || diag.OwnerReferences[0].Name != pod.Name {
		t.Fatalf("expected owner reference to pod, got %+v", diag.OwnerReferences)
	}
}

func TestReconcile_SkipsHealthyPod(t *testing.T) {
	scheme := newTestScheme(t)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "healthy", Namespace: "default"},
