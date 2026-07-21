package controller

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	diagv1alpha1 "github.com/chenar/poddoctor/api/v1alpha1"
	"github.com/chenar/poddoctor/internal/diagnosis"
)

// triggerReasons are the container waiting-state reasons that identify a
// pod stuck in a failure loop worth diagnosing.
var triggerReasons = map[string]bool{
	"CrashLoopBackOff": true,
	"ImagePullBackOff": true,
	"ErrImagePull":     true,
	"InvalidImageName": true,
}

// diagnosisRequeueInterval bounds how often an already-diagnosed episode is
// re-checked for a new restart (i.e. a new failure episode).
const diagnosisRequeueInterval = 2 * time.Minute

// PodDiagnosisReconciler watches Pods for CrashLoopBackOff/ImagePullBackOff,
// gathers evidence (events, previous logs, rollout timing), runs the
// rule-based diagnosis engine, and records the result as an owned
// PodDiagnosis CR plus a Kubernetes Event on the Pod.
type PodDiagnosisReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// ClientSet is used for subresources the controller-runtime client
	// doesn't expose: pod logs and the Events "Search" helper.
	ClientSet kubernetes.Interface
	Recorder  events.EventRecorder

	LogTailLines  int64
	RolloutWindow time.Duration
}

// +kubebuilder:rbac:groups=diagnostics.poddoctor.dev,resources=poddiagnoses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=diagnostics.poddoctor.dev,resources=poddiagnoses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch

// Reconcile handles one Pod key: it diagnoses the failing container (if any)
// and writes/updates the corresponding PodDiagnosis object.
func (r *PodDiagnosisReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if pod.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	cs, failing := findFailingContainer(pod.Status.ContainerStatuses)
	if !failing {
		return ctrl.Result{}, nil
	}

	diagKey := client.ObjectKey{Namespace: pod.Namespace, Name: pod.Name}
	var diagObj diagv1alpha1.PodDiagnosis
	getErr := r.Get(ctx, diagKey, &diagObj)
	isNew := apierrors.IsNotFound(getErr)
	if getErr != nil && !isNew {
		return ctrl.Result{}, getErr
	}

	// Already diagnosed this failure episode (restart count unchanged since
	// last diagnosis) — nothing new to say, just recheck later.
	if !isNew && diagObj.Status.LastDiagnosedRestartCount == cs.RestartCount {
		return ctrl.Result{RequeueAfter: diagnosisRequeueInterval}, nil
	}

	if isNew {
		diagObj = diagv1alpha1.PodDiagnosis{
			ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
		}
	}

	diagObj.Spec = diagv1alpha1.PodDiagnosisSpec{
		PodName:       pod.Name,
		PodNamespace:  pod.Namespace,
		ContainerName: cs.Name,
		PodUID:        pod.UID,
	}
	if err := ctrl.SetControllerReference(&pod, &diagObj, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	if isNew {
		if err := r.Create(ctx, &diagObj); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	} else if err := r.Update(ctx, &diagObj); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	ev := r.gatherEvidence(ctx, &pod, cs)
	result := diagnosis.Diagnose(ev)

	now := metav1.Now()
	firstObserved := diagObj.Status.FirstObserved
	if firstObserved.IsZero() {
		firstObserved = now
	}

	diagObj.Status = diagv1alpha1.PodDiagnosisStatus{
		Phase:                     "Diagnosed",
		RootCause:                 result.RootCause,
		Confidence:                result.Confidence,
		Summary:                   result.Summary,
		Recommendation:            result.Recommendation,
		ExitCode:                  exitCodePtr(cs),
		TerminationReason:         terminationReasonOf(cs),
		RestartCount:              cs.RestartCount,
		LastDiagnosedRestartCount: cs.RestartCount,
		RecentEvents:              ev.RecentEvents,
		LogExcerpt:                ev.LogTail,
		RolloutContext:            ev.RolloutContext,
		FirstObserved:             firstObserved,
		LastObserved:              now,
	}

	if err := r.Status().Update(ctx, &diagObj); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	if r.Recorder != nil {
		r.Recorder.Eventf(&pod, nil, corev1.EventTypeWarning, "CrashLoopDiagnosed", "Diagnose",
			"[%s/%s] %s %s", result.RootCause, result.Confidence, result.Summary, result.Recommendation)
	}

	logger.Info("diagnosed failing container",
		"pod", pod.Name, "container", cs.Name, "rootCause", result.RootCause,
		"confidence", result.Confidence, "restarts", cs.RestartCount)

	return ctrl.Result{RequeueAfter: diagnosisRequeueInterval}, nil
}

func (r *PodDiagnosisReconciler) gatherEvidence(ctx context.Context, pod *corev1.Pod, cs corev1.ContainerStatus) diagnosis.Evidence {
	ev := diagnosis.Evidence{RestartCount: cs.RestartCount}

	if cs.State.Waiting != nil {
		ev.WaitingReason = cs.State.Waiting.Reason
		ev.WaitingMessage = cs.State.Waiting.Message
	}
	if t := cs.LastTerminationState.Terminated; t != nil {
		ev.HasTerminated = true
		ev.TerminatedReason = t.Reason
		ev.TerminatedMessage = t.Message
		ev.ExitCode = t.ExitCode
	}

	ev.RecentEvents = r.recentEvents(ctx, pod)
	ev.LogTail = r.logTail(ctx, pod, cs.Name)
	ev.RecentRollout, ev.RolloutContext = r.rolloutContext(ctx, pod)

	return ev
}

// recentEvents fetches the newest Kubernetes Events involving this pod,
// via the same Search helper `kubectl describe pod` uses under the hood.
func (r *PodDiagnosisReconciler) recentEvents(ctx context.Context, pod *corev1.Pod) []diagv1alpha1.EvidenceEvent {
	logger := log.FromContext(ctx)
	if r.ClientSet == nil {
		return nil
	}

	list, err := r.ClientSet.CoreV1().Events(pod.Namespace).SearchWithContext(ctx, r.Scheme, pod)
	if err != nil {
		logger.V(1).Info("could not fetch events for pod", "pod", pod.Name, "error", err.Error())
		return nil
	}

	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[j].LastTimestamp.Before(&list.Items[i].LastTimestamp)
	})

	const maxEvents = 5
	n := len(list.Items)
	if n > maxEvents {
		n = maxEvents
	}
	out := make([]diagv1alpha1.EvidenceEvent, 0, n)
	for i := 0; i < n; i++ {
		e := list.Items[i]
		out = append(out, diagv1alpha1.EvidenceEvent{
			Reason:        e.Reason,
			Message:       e.Message,
			Count:         e.Count,
			LastTimestamp: e.LastTimestamp,
		})
	}
	return out
}

// logTail returns the tail of the crashed container's previous run,
// truncated to a bounded size to keep the CR small. Best-effort: returns
// empty string if no previous instance exists or logs aren't fetchable yet.
func (r *PodDiagnosisReconciler) logTail(ctx context.Context, pod *corev1.Pod, container string) string {
	logger := log.FromContext(ctx)
	if r.ClientSet == nil {
		return ""
	}

	tail := r.LogTailLines
	if tail <= 0 {
		tail = 50
