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
