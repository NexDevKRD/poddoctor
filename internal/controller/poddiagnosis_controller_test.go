package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
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
	"github.com/chenar/poddoctor/internal/alertgroup"
	"github.com/chenar/poddoctor/internal/notify"
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

	diagKey := client.ObjectKey{Namespace: pod.Namespace, Name: diagnosisName(pod.Name, "app")}
	var diag diagv1alpha1.PodDiagnosis
	if err := r.Get(ctx, diagKey, &diag); err != nil {
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
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	r := newReconciler(t, scheme, pod)

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)}
	res, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Fatalf("expected no requeue for healthy pod, got %+v", res)
	}

	diagKey := client.ObjectKey{Namespace: pod.Namespace, Name: diagnosisName(pod.Name, "app")}
	var diag diagv1alpha1.PodDiagnosis
	if err := r.Get(ctx, diagKey, &diag); err == nil {
		t.Fatalf("expected no PodDiagnosis for healthy pod")
	}
}

func TestReconcile_DedupesSameEpisode(t *testing.T) {
	scheme := newTestScheme(t)
	pod := oomKilledPod()
	r := newReconciler(t, scheme, pod)

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)}
	diagKey := client.ObjectKey{Namespace: pod.Namespace, Name: diagnosisName(pod.Name, "app")}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}

	var first diagv1alpha1.PodDiagnosis
	if err := r.Get(ctx, diagKey, &first); err != nil {
		t.Fatalf("get after first reconcile: %v", err)
	}
	firstObserved := first.Status.FirstObserved

	// Same restart count -> second reconcile should be a cheap no-op (dedup),
	// leaving FirstObserved untouched.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}

	var second diagv1alpha1.PodDiagnosis
	if err := r.Get(ctx, diagKey, &second); err != nil {
		t.Fatalf("get after second reconcile: %v", err)
	}
	if !second.Status.FirstObserved.Time.Equal(firstObserved.Time) {
		t.Fatalf("FirstObserved changed on dedup: %v -> %v", firstObserved, second.Status.FirstObserved)
	}
}

func TestReconcile_DiagnosesInitContainer(t *testing.T) {
	scheme := newTestScheme(t)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "migrator", Namespace: "default", UID: types.UID("pod-uid-2")},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "run-migrations",
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1},
					},
				},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}},
			},
		},
	}
	r := newReconciler(t, scheme, pod)

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	diagKey := client.ObjectKey{Namespace: pod.Namespace, Name: diagnosisName(pod.Name, "run-migrations")}
	var diag diagv1alpha1.PodDiagnosis
	if err := r.Get(ctx, diagKey, &diag); err != nil {
		t.Fatalf("expected PodDiagnosis for failing init container: %v", err)
	}
	if diag.Status.RootCause != diagv1alpha1.RootCauseApplicationError {
		t.Fatalf("RootCause = %s, want %s", diag.Status.RootCause, diagv1alpha1.RootCauseApplicationError)
	}
}

func TestReconcile_DiagnosesEachFailingContainerSeparately(t *testing.T) {
	scheme := newTestScheme(t)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: "default", UID: types.UID("pod-uid-3")},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "app",
					RestartCount: 3,
					State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
					},
				},
				{
					Name:         "sidecar",
					RestartCount: 5,
					State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1},
					},
				},
			},
		},
	}
	r := newReconciler(t, scheme, pod)

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	for _, name := range []string{"app", "sidecar"} {
		diagKey := client.ObjectKey{Namespace: pod.Namespace, Name: diagnosisName(pod.Name, name)}
		var diag diagv1alpha1.PodDiagnosis
		if err := r.Get(ctx, diagKey, &diag); err != nil {
			t.Fatalf("expected PodDiagnosis for container %q: %v", name, err)
		}
		if diag.Spec.ContainerName != name {
			t.Fatalf("ContainerName = %s, want %s", diag.Spec.ContainerName, name)
		}
	}
}

func TestReconcile_RolloutContext_StatefulSet(t *testing.T) {
	scheme := newTestScheme(t)
	now := metav1.Now()

	rev := &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "db-6c9f7d",
			Namespace:         "default",
			CreationTimestamp: now,
		},
		Revision: 4,
	}

	pod := oomKilledPod()
	pod.Name = "db-0"
	pod.Labels = map[string]string{"controller-revision-hash": rev.Name}
	pod.OwnerReferences = []metav1.OwnerReference{{Kind: "StatefulSet", Name: "db"}}
	pod.CreationTimestamp = metav1.NewTime(now.Add(30 * time.Second))

	r := newReconciler(t, scheme, pod, rev)

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	diagKey := client.ObjectKey{Namespace: pod.Namespace, Name: diagnosisName(pod.Name, "app")}
	var diag diagv1alpha1.PodDiagnosis
	if err := r.Get(ctx, diagKey, &diag); err != nil {
		t.Fatalf("get diagnosis: %v", err)
	}
	if diag.Status.RolloutContext == "" {
		t.Fatalf("expected RolloutContext to be set for pod started shortly after StatefulSet revision rollout")
	}
}

func TestReconcile_NodeMemoryPressureEvidence(t *testing.T) {
	scheme := newTestScheme(t)
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}
	pod := oomKilledPod()
	pod.Spec.NodeName = node.Name
	r := newReconciler(t, scheme, pod, node)

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	diagKey := client.ObjectKey{Namespace: pod.Namespace, Name: diagnosisName(pod.Name, "app")}
	var diag diagv1alpha1.PodDiagnosis
	if err := r.Get(ctx, diagKey, &diag); err != nil {
		t.Fatalf("get diagnosis: %v", err)
	}
	if diag.Status.Confidence != diagv1alpha1.ConfidenceHigh {
		t.Fatalf("Confidence = %s, want High (node MemoryPressure should upgrade an OOMKilled diagnosis)", diag.Status.Confidence)
	}
}

func TestReconcile_SendsWebhookNotification(t *testing.T) {
	scheme := newTestScheme(t)
	var received int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		atomic.AddInt32(&received, 1)
		var p notify.Payload
		if err := json.NewDecoder(req.Body).Decode(&p); err != nil {
			t.Errorf("decode webhook body: %v", err)
		}
		if p.Cluster != "us-east-1" {
			t.Errorf("Cluster = %q, want us-east-1", p.Cluster)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pod := oomKilledPod()
	r := newReconciler(t, scheme, pod)
	r.ClusterName = "us-east-1"
	r.NotifyRouter = notify.NewRouter(srv.URL, notify.FormatGeneric, "", nil)
	r.Grouper = alertgroup.New(time.Minute)

	ctx := context.Background()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if got := atomic.LoadInt32(&received); got != 1 {
		t.Fatalf("webhook calls = %d, want 1", got)
	}
}

func TestReconcile_GroupsRepeatedNotifications(t *testing.T) {
	scheme := newTestScheme(t)
	var received int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		atomic.AddInt32(&received, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	router := notify.NewRouter(srv.URL, notify.FormatGeneric, "", nil)
	grouper := alertgroup.New(time.Minute)
	ctx := context.Background()

	// Two different pods, same namespace + root cause: the second should
	// be folded into the first's notification (grouped), not send its own.
	for i, name := range []string{"payments-api-a", "payments-api-b"} {
		pod := oomKilledPod()
		pod.Name = name
		r := newReconciler(t, scheme, pod)
		r.NotifyRouter = router
		r.Grouper = grouper

		req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(pod)}
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("Reconcile() #%d error = %v", i, err)
		}
	}

	if got := atomic.LoadInt32(&received); got != 1 {
		t.Fatalf("webhook calls = %d, want 1 (second occurrence should be grouped/suppressed)", got)
	}
}
