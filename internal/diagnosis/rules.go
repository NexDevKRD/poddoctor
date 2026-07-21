// Package diagnosis implements PodDoctor's rule-based root-cause engine.
// It is a pure function over collected evidence with no cluster access,
// which keeps it fast, deterministic, and independently testable.
package diagnosis

import (
	"fmt"
	"strings"

	diagv1alpha1 "github.com/chenar/poddoctor/api/v1alpha1"
)

// Evidence is everything the controller gathered about one failing container.
type Evidence struct {
	// WaitingReason/WaitingMessage come from ContainerStatus.State.Waiting.
	WaitingReason  string
	WaitingMessage string

	// HasTerminated/TerminatedReason/TerminatedMessage/ExitCode come from
	// ContainerStatus.LastTerminationState.Terminated.
	HasTerminated     bool
	TerminatedReason  string
	TerminatedMessage string
	ExitCode          int32

	RestartCount int32

	// RecentEvents are the pod's most recent Kubernetes Events, newest first.
	RecentEvents []diagv1alpha1.EvidenceEvent

	// LogTail is the tail of the previous container instance's logs (may be empty).
	LogTail string

	// RecentRollout is true when the pod started shortly after its owning
	// Deployment/ReplicaSet rolled out a new revision.
	RecentRollout bool
	// RolloutContext is a human-readable description of that rollout, if RecentRollout.
	RolloutContext string
}

// Result is the operator's determination for one failure episode.
type Result struct {
	RootCause      diagv1alpha1.RootCause
	Confidence     diagv1alpha1.Confidence
	Summary        string
	Recommendation string
}

// Diagnose applies precedence-ordered heuristics to Evidence and returns the
// best-effort root cause. Rules are checked most-specific-signal first;
// the first rule that matches wins, with rollout context appended as a
// contributing factor when present.
func Diagnose(ev Evidence) Result {
	res := diagnosePrimary(ev)

	if ev.RecentRollout {
		if res.RootCause == diagv1alpha1.RootCauseUnknown {
			res = Result{
				RootCause:      diagv1alpha1.RootCauseRecentRollout,
				Confidence:     diagv1alpha1.ConfidenceMedium,
				Summary:        fmt.Sprintf("No specific failure signature found, but the pod started shortly after a rollout. %s", ev.RolloutContext),
				Recommendation: "This looks like a regression introduced by the latest rollout. Consider `kubectl rollout undo` and compare the previous and current image/config.",
			}
		} else {
			res.Summary = fmt.Sprintf("%s Also: %s", res.Summary, ev.RolloutContext)
			res.Recommendation = fmt.Sprintf("%s A recent rollout may be the trigger; consider `kubectl rollout undo` if this started right after a deploy.", res.Recommendation)
		}
	}

	if probeFailing(ev.RecentEvents) && (res.RootCause == diagv1alpha1.RootCauseSignalKilled || res.RootCause == diagv1alpha1.RootCauseApplicationError || res.RootCause == diagv1alpha1.RootCauseUnknown) {
		res = Result{
			RootCause:      diagv1alpha1.RootCauseProbeFailure,
			Confidence:     diagv1alpha1.ConfidenceHigh,
			Summary:        "Liveness/readiness probe is repeatedly failing, causing the kubelet to kill the container.",
			Recommendation: "Check the probe path/port/timeout against the app's actual startup time. Increase initialDelaySeconds or periodSeconds, or fix the health endpoint.",
		}
	}

	if res.Summary == "" {
		res.Summary = "No known failure signature matched this container's state."
		res.Recommendation = "Inspect the attached recent events and log excerpt manually; this failure pattern isn't yet covered by a rule."
	}
	if res.Confidence == "" {
		res.Confidence = diagv1alpha1.ConfidenceLow
	}
	if len(ev.RecentEvents) == 0 && ev.LogTail == "" && !ev.RecentRollout && res.Confidence == diagv1alpha1.ConfidenceMedium {
		res.Confidence = diagv1alpha1.ConfidenceLow
	}

	return res
}

func diagnosePrimary(ev Evidence) Result {
	switch ev.WaitingReason {
	case "ImagePullBackOff", "ErrImagePull":
		return Result{
			RootCause:      diagv1alpha1.RootCauseImagePullError,
			Confidence:     diagv1alpha1.ConfidenceHigh,
			Summary:        fmt.Sprintf("Container image cannot be pulled: %s", firstNonEmpty(ev.WaitingMessage, ev.WaitingReason)),
			Recommendation: "Verify the image name and tag exist in the registry, and that imagePullSecrets grants access to it from this cluster.",
		}
	case "InvalidImageName":
		return Result{
			RootCause:      diagv1alpha1.RootCauseImagePullError,
			Confidence:     diagv1alpha1.ConfidenceHigh,
			Summary:        fmt.Sprintf("Container image reference is invalid: %s", firstNonEmpty(ev.WaitingMessage, ev.WaitingReason)),
			Recommendation: "Fix the image field in the pod spec (typo, missing registry, or invalid tag).",
		}
