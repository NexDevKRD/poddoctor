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
