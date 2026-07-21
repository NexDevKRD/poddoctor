package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// RootCause identifies the diagnosed reason a container is failing.
// +kubebuilder:validation:Enum=OOMKilled;ImagePullError;BadCommand;SegFault;SignalKilled;ProbeFailure;RecentRolloutRegression;ApplicationError;Unknown
type RootCause string

const (
	RootCauseOOMKilled        RootCause = "OOMKilled"
	RootCauseImagePullError   RootCause = "ImagePullError"
	RootCauseBadCommand       RootCause = "BadCommand"
	RootCauseSegFault         RootCause = "SegFault"
	RootCauseSignalKilled     RootCause = "SignalKilled"
	RootCauseProbeFailure     RootCause = "ProbeFailure"
	RootCauseRecentRollout    RootCause = "RecentRolloutRegression"
	RootCauseApplicationError RootCause = "ApplicationError"
	RootCauseUnknown          RootCause = "Unknown"
)

// Confidence describes how certain the diagnosis is, based on how much
// corroborating evidence (events, logs, rollout timing) was available.
// +kubebuilder:validation:Enum=High;Medium;Low
type Confidence string

const (
	ConfidenceHigh   Confidence = "High"
	ConfidenceMedium Confidence = "Medium"
	ConfidenceLow    Confidence = "Low"
)

// PodDiagnosisSpec identifies the pod/container this diagnosis covers.
// PodDiagnosis objects are created and owned by the operator; spec is
// written once at creation and not intended for user edits.
type PodDiagnosisSpec struct {
	// PodName is the name of the diagnosed Pod.
	PodName string `json:"podName"`

	// PodNamespace is the namespace of the diagnosed Pod.
	PodNamespace string `json:"podNamespace"`

	// ContainerName is the container within the Pod that is crash-looping.
	ContainerName string `json:"containerName"`

	// PodUID is the UID of the diagnosed Pod at time of diagnosis.
	PodUID types.UID `json:"podUID,omitempty"`
}

// EvidenceEvent is a condensed Kubernetes Event used as supporting evidence.
type EvidenceEvent struct {
	Reason        string      `json:"reason"`
	Message       string      `json:"message"`
	Count         int32       `json:"count,omitempty"`
	LastTimestamp metav1.Time `json:"lastTimestamp,omitempty"`
}

// PodDiagnosisStatus is the diagnosis result, written by the operator.
type PodDiagnosisStatus struct {
	// Phase is the diagnosis lifecycle: Diagnosing or Diagnosed.
	// +kubebuilder:validation:Enum=Diagnosing;Diagnosed
	Phase string `json:"phase,omitempty"`

	// RootCause is the operator's best determination of why the container is failing.
	RootCause RootCause `json:"rootCause,omitempty"`

	// Confidence indicates how much corroborating evidence backs RootCause.
	Confidence Confidence `json:"confidence,omitempty"`

	// Summary is a one-line human-readable explanation of the failure.
	Summary string `json:"summary,omitempty"`

	// Recommendation is a suggested next step to resolve the failure.
	Recommendation string `json:"recommendation,omitempty"`

	// ExitCode is the container's last exit code, if it ever terminated.
	ExitCode *int32 `json:"exitCode,omitempty"`

	// TerminationReason is the raw Kubernetes termination/waiting reason
	// (e.g. OOMKilled, Error, CrashLoopBackOff, ImagePullBackOff).
	TerminationReason string `json:"terminationReason,omitempty"`

	// RestartCount is the container restart count at last diagnosis.
	RestartCount int32 `json:"restartCount,omitempty"`

	// LastDiagnosedRestartCount is the restart count already diagnosed,
	// used to avoid re-diagnosing the same failure episode.
	LastDiagnosedRestartCount int32 `json:"lastDiagnosedRestartCount,omitempty"`

	// RecentEvents are the most recent Kubernetes Events for the pod, used as evidence.
	RecentEvents []EvidenceEvent `json:"recentEvents,omitempty"`

	// LogExcerpt is the tail of the previous container instance's logs, truncated.
	LogExcerpt string `json:"logExcerpt,omitempty"`

	// RolloutContext describes a recent Deployment rollout correlated with the failure, if any.
	RolloutContext string `json:"rolloutContext,omitempty"`

	// FirstObserved is when this failure episode was first diagnosed.
	FirstObserved metav1.Time `json:"firstObserved,omitempty"`

	// LastObserved is when this diagnosis was last updated.
	LastObserved metav1.Time `json:"lastObserved,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Pod",type=string,JSONPath=`.spec.podName`
// +kubebuilder:printcolumn:name="Root Cause",type=string,JSONPath=`.status.rootCause`
// +kubebuilder:printcolumn:name="Confidence",type=string,JSONPath=`.status.confidence`
// +kubebuilder:printcolumn:name="Restarts",type=integer,JSONPath=`.status.restartCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:shortName=pd

// PodDiagnosis is the Schema for the poddiagnoses API. Each instance holds
// the operator's root-cause analysis for one crash-looping Pod container.
type PodDiagnosis struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PodDiagnosisSpec   `json:"spec,omitempty"`
	Status PodDiagnosisStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PodDiagnosisList contains a list of PodDiagnosis.
type PodDiagnosisList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PodDiagnosis `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(GroupVersion, &PodDiagnosis{}, &PodDiagnosisList{})
		metav1.AddToGroupVersion(scheme, GroupVersion)
		return nil
	})
}
