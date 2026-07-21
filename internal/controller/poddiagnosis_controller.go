package controller

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"golang.org/x/time/rate"
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
	"github.com/chenar/poddoctor/internal/alertgroup"
	"github.com/chenar/poddoctor/internal/diagnosis"
	"github.com/chenar/poddoctor/internal/metrics"
	"github.com/chenar/poddoctor/internal/notify"
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

// logFetchTimeout bounds how long a single previous-logs fetch may block a
// reconcile, so a slow/hanging kubelet log stream can't wedge a worker.
const logFetchTimeout = 5 * time.Second

// webhookTimeout bounds how long the optional outbound notification may
// block a reconcile.
const webhookTimeout = 5 * time.Second

// PodDiagnosisReconciler watches Pods for CrashLoopBackOff/ImagePullBackOff
// (in init or regular containers), gathers evidence (events, previous logs,
// rollout timing), runs the rule-based diagnosis engine, and records the
// result as an owned PodDiagnosis CR plus a Kubernetes Event on the Pod.
type PodDiagnosisReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// ClientSet is used for subresources the controller-runtime client
	// doesn't expose: pod logs and the Events "Search" helper.
	ClientSet kubernetes.Interface
	Recorder  events.EventRecorder

	LogTailLines  int64
	RolloutWindow time.Duration

	// ClusterName identifies this cluster in outbound notifications (see
	// internal/notify.Payload.Cluster) — set it when notifications feed a
	// fleet hub aggregating multiple clusters.
	ClusterName string

	// NotifyRouter picks the webhook (if any) each namespace's diagnoses
	// go to. Built by the caller (see cmd/main.go); nil disables
	// notifications entirely.
	NotifyRouter *notify.Router

	// Grouper folds repeated diagnoses with the same (namespace, root
	// cause) within a window into one notification. Defaulted in
	// SetupWithManager if nil.
	Grouper          *alertgroup.Grouper
	AlertGroupWindow time.Duration

	// EvidenceLimiter bounds how fast the controller hits the apiserver
	// for evidence (Events search, previous logs) — protects the
	// apiserver from a self-inflicted request storm when many pods fail
	// at once. Defaulted in SetupWithManager if nil.
	EvidenceLimiter *rate.Limiter
	EvidenceQPS     float64
}

// +kubebuilder:rbac:groups=diagnostics.poddoctor.dev,resources=poddiagnoses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=diagnostics.poddoctor.dev,resources=poddiagnoses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=controllerrevisions,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

// failingContainer is one container (init or regular) currently stuck in a
// trigger reason, alongside the status PodDoctor needs to diagnose it.
type failingContainer struct {
	status corev1.ContainerStatus
	isInit bool
}

// Reconcile handles one Pod key: it diagnoses every failing container (init
// or regular) and writes/updates one PodDiagnosis object per container.
func (r *PodDiagnosisReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	failing := findFailingContainers(&pod)
	if len(failing) == 0 {
		return ctrl.Result{}, nil
	}

	for _, fc := range failing {
		needsRequeue, err := r.reconcileContainer(ctx, &pod, fc)
		if err != nil {
			return ctrl.Result{}, err
		}
		if needsRequeue {
			return ctrl.Result{Requeue: true}, nil
		}
	}

	return ctrl.Result{RequeueAfter: diagnosisRequeueInterval}, nil
}

// reconcileContainer diagnoses a single failing container and writes its
// PodDiagnosis. needsRequeue is true on an optimistic-lock conflict, meaning
// the caller should stop and let the next reconcile retry from scratch.
func (r *PodDiagnosisReconciler) reconcileContainer(ctx context.Context, pod *corev1.Pod, fc failingContainer) (bool, error) {
	logger := log.FromContext(ctx)
	cs := fc.status

	diagKey := client.ObjectKey{Namespace: pod.Namespace, Name: diagnosisName(pod.Name, cs.Name)}
	var diagObj diagv1alpha1.PodDiagnosis
	getErr := r.Get(ctx, diagKey, &diagObj)
	isNew := apierrors.IsNotFound(getErr)
	if getErr != nil && !isNew {
		return false, getErr
	}

	// Already diagnosed this failure episode (restart count unchanged since
	// last diagnosis) — nothing new to say, just recheck later.
	if !isNew && diagObj.Status.LastDiagnosedRestartCount == cs.RestartCount {
		return false, nil
	}

	if isNew {
		diagObj = diagv1alpha1.PodDiagnosis{
			ObjectMeta: metav1.ObjectMeta{Name: diagKey.Name, Namespace: pod.Namespace},
		}
	}

	diagObj.Spec = diagv1alpha1.PodDiagnosisSpec{
		PodName:       pod.Name,
		PodNamespace:  pod.Namespace,
		ContainerName: cs.Name,
		PodUID:        pod.UID,
	}
	if err := ctrl.SetControllerReference(pod, &diagObj, r.Scheme); err != nil {
		return false, err
	}

	if isNew {
		if err := r.Create(ctx, &diagObj); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return true, nil
			}
			return false, err
		}
	} else if err := r.Update(ctx, &diagObj); err != nil {
		if apierrors.IsConflict(err) {
			return true, nil
		}
		return false, err
	}

	ev := r.gatherEvidence(ctx, pod, cs)
	result := diagnosis.Diagnose(ev)
	if fc.isInit {
		result.Summary = "[init container] " + result.Summary
	}

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
			return true, nil
		}
		return false, err
	}

	metrics.DiagnosesTotal.WithLabelValues(string(result.RootCause), string(result.Confidence)).Inc()

	if r.Recorder != nil {
		r.Recorder.Eventf(pod, nil, corev1.EventTypeWarning, "CrashLoopDiagnosed", "Diagnose",
			"[%s/%s] %s %s", result.RootCause, result.Confidence, result.Summary, result.Recommendation)
	}

	// ponytail: synchronous, bounded by webhookTimeout — simplest correct
	// option for one notification per diagnosis; move to an async queue if
	// webhook latency starts measurably slowing reconciles down.
	if url, format, token, ok := r.NotifyRouter.Route(pod.Namespace); ok {
		groupKey := pod.Namespace + "/" + string(result.RootCause)
		if shouldNotify, suppressed := r.Grouper.Observe(groupKey); shouldNotify {
			notifyCtx, cancel := context.WithTimeout(ctx, webhookTimeout)
			n := notify.Notification{Diag: &diagObj, Cluster: r.ClusterName, SuppressedCount: suppressed}
			err := notify.Send(notifyCtx, url, format, token, n)
			cancel()
			if err != nil {
				logger.V(1).Info("webhook notification failed", "pod", pod.Name, "container", cs.Name, "error", err.Error())
			}
		}
	}

	logger.Info("diagnosed failing container",
		"pod", pod.Name, "container", cs.Name, "init", fc.isInit, "rootCause", result.RootCause,
		"confidence", result.Confidence, "restarts", cs.RestartCount)

	return false, nil
}

func diagnosisName(podName, containerName string) string {
	return podName + "-" + containerName
}

func (r *PodDiagnosisReconciler) gatherEvidence(ctx context.Context, pod *corev1.Pod, cs corev1.ContainerStatus) diagnosis.Evidence {
	logger := log.FromContext(ctx)
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

	// Bounds how fast evidence-gathering hits the apiserver — protects it
	// from a self-inflicted request storm when a bad rollout crash-loops
	// many pods at once.
	if r.EvidenceLimiter != nil {
		if err := r.EvidenceLimiter.Wait(ctx); err != nil {
			logger.V(1).Info("evidence rate limiter wait interrupted", "error", err.Error())
		}
	}

	ev.RecentEvents = r.recentEvents(ctx, pod)
	ev.LogTail = r.logTail(ctx, pod, cs.Name)
	ev.RecentRollout, ev.RolloutContext = r.rolloutContext(ctx, pod)
	ev.NodePressureConditions, ev.NodeNotReady = r.nodeEvidence(ctx, pod)

	return ev
}

// nodeEvidence reports abnormal conditions on the pod's node, if any —
// distinguishing "this container has a bug" from "the node it landed on
// is under memory/disk/PID pressure or NotReady", which usually affects
// every pod on that node, not just this one.
func (r *PodDiagnosisReconciler) nodeEvidence(ctx context.Context, pod *corev1.Pod) (pressures []string, notReady bool) {
	if pod.Spec.NodeName == "" {
		return nil, false
	}

	var node corev1.Node
	if err := r.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, &node); err != nil {
		return nil, false
	}

	for _, cond := range node.Status.Conditions {
		switch cond.Type {
		case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure:
			if cond.Status == corev1.ConditionTrue {
				pressures = append(pressures, string(cond.Type))
			}
		case corev1.NodeReady:
			notReady = cond.Status != corev1.ConditionTrue
		}
	}
	return pressures, notReady
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
// empty string if no previous instance exists or logs aren't fetchable
// within logFetchTimeout.
func (r *PodDiagnosisReconciler) logTail(ctx context.Context, pod *corev1.Pod, container string) string {
	logger := log.FromContext(ctx)
	if r.ClientSet == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, logFetchTimeout)
	defer cancel()

	tail := r.LogTailLines
	if tail <= 0 {
		tail = 50
	}

	req := r.ClientSet.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
		Container: container,
		Previous:  true,
		TailLines: &tail,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		logger.V(1).Info("could not fetch previous logs", "pod", pod.Name, "container", container, "error", err.Error())
		return ""
	}
	defer func() { _ = stream.Close() }()

	const maxBytes = 8192
	buf := make([]byte, maxBytes)
	n, readErr := io.ReadFull(stream, buf)
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		logger.V(1).Info("error reading previous logs", "pod", pod.Name, "container", container, "error", readErr.Error())
	}

	text := string(buf[:n])
	const maxLen = 4000
	if len(text) > maxLen {
		text = text[len(text)-maxLen:]
	}
	return text
}

// rolloutContext reports whether the pod started shortly after its owning
// workload rolled out a new revision — a strong hint that a recent deploy
// introduced the failure. Deployment-managed pods are correlated via their
// ReplicaSet's creation time; StatefulSet/DaemonSet-managed pods (which
// have no intermediate ReplicaSet) are correlated via their
// controller-revision-hash ControllerRevision instead.
func (r *PodDiagnosisReconciler) rolloutContext(ctx context.Context, pod *corev1.Pod) (bool, string) {
	if rsRef := findOwner(pod.OwnerReferences, "ReplicaSet"); rsRef != nil {
		return r.rolloutContextFromReplicaSet(ctx, pod, rsRef)
	}
	if owner := findOwnerAny(pod.OwnerReferences, "StatefulSet", "DaemonSet"); owner != nil {
		return r.rolloutContextFromControllerRevision(ctx, pod, owner)
	}
	return false, ""
}

func (r *PodDiagnosisReconciler) rolloutContextFromReplicaSet(ctx context.Context, pod *corev1.Pod, rsRef *metav1.OwnerReference) (bool, string) {
	var rs appsv1.ReplicaSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: rsRef.Name}, &rs); err != nil {
		return false, ""
	}

	window := r.rolloutWindow()
	if pod.CreationTimestamp.After(rs.CreationTimestamp.Add(window)) {
		return false, ""
	}

	revision := rs.Annotations["deployment.kubernetes.io/revision"]
	elapsed := pod.CreationTimestamp.Sub(rs.CreationTimestamp.Time).Round(time.Second)

	deployRef := findOwner(rs.OwnerReferences, "Deployment")
	if deployRef == nil {
		return true, fmt.Sprintf("pod started %s after replicaset %q (revision %s) was created", elapsed, rs.Name, revision)
	}
	return true, fmt.Sprintf("pod started %s after deployment %q rolled out replicaset %q (revision %s)", elapsed, deployRef.Name, rs.Name, revision)
}

func (r *PodDiagnosisReconciler) rolloutContextFromControllerRevision(ctx context.Context, pod *corev1.Pod, owner *metav1.OwnerReference) (bool, string) {
	revName := pod.Labels["controller-revision-hash"]
	if revName == "" {
		return false, ""
	}

	var rev appsv1.ControllerRevision
	if err := r.Get(ctx, client.ObjectKey{Namespace: pod.Namespace, Name: revName}, &rev); err != nil {
		return false, ""
	}

	window := r.rolloutWindow()
	if pod.CreationTimestamp.After(rev.CreationTimestamp.Add(window)) {
		return false, ""
	}

	elapsed := pod.CreationTimestamp.Sub(rev.CreationTimestamp.Time).Round(time.Second)
	return true, fmt.Sprintf("pod started %s after %s %q rolled out revision %q (revision %d)", elapsed, owner.Kind, owner.Name, rev.Name, rev.Revision)
}

func (r *PodDiagnosisReconciler) rolloutWindow() time.Duration {
	if r.RolloutWindow <= 0 {
		return 10 * time.Minute
	}
	return r.RolloutWindow
}

func findOwner(refs []metav1.OwnerReference, kind string) *metav1.OwnerReference {
	return findOwnerAny(refs, kind)
}

func findOwnerAny(refs []metav1.OwnerReference, kinds ...string) *metav1.OwnerReference {
	for i := range refs {
		for _, kind := range kinds {
			if refs[i].Kind == kind {
				return &refs[i]
			}
		}
	}
	return nil
}

// findFailingContainers returns every init or regular container currently
// stuck in a trigger reason (e.g. CrashLoopBackOff, ImagePullBackOff).
// Init containers are checked first since they block the pod from starting
// at all.
func findFailingContainers(pod *corev1.Pod) []failingContainer {
	var out []failingContainer
	for _, cs := range pod.Status.InitContainerStatuses {
		if cs.State.Waiting != nil && triggerReasons[cs.State.Waiting.Reason] {
			out = append(out, failingContainer{status: cs, isInit: true})
		}
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && triggerReasons[cs.State.Waiting.Reason] {
			out = append(out, failingContainer{status: cs})
		}
	}
	return out
}

func terminationReasonOf(cs corev1.ContainerStatus) string {
	if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
		return cs.State.Waiting.Reason
	}
	if cs.LastTerminationState.Terminated != nil {
		return cs.LastTerminationState.Terminated.Reason
	}
	return ""
}

func exitCodePtr(cs corev1.ContainerStatus) *int32 {
	if cs.LastTerminationState.Terminated == nil {
		return nil
	}
	code := cs.LastTerminationState.Terminated.ExitCode
	return &code
}

func failingPodPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return false
		}
		return len(findFailingContainers(pod)) > 0
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *PodDiagnosisReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.LogTailLines <= 0 {
		r.LogTailLines = 50
	}
	if r.RolloutWindow <= 0 {
		r.RolloutWindow = 10 * time.Minute
	}
	if r.Grouper == nil {
		window := r.AlertGroupWindow
		if window <= 0 {
			window = 2 * time.Minute
		}
		r.Grouper = alertgroup.New(window)
	}
	if r.EvidenceLimiter == nil {
		qps := r.EvidenceQPS
		if qps <= 0 {
			qps = 20
		}
		r.EvidenceLimiter = rate.NewLimiter(rate.Limit(qps), int(qps*2))
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("poddiagnosis").
		For(&corev1.Pod{}, builder.WithPredicates(failingPodPredicate())).
		Owns(&diagv1alpha1.PodDiagnosis{}).
		Complete(r)
}
