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

	// NodePressureConditions lists any abnormal Node conditions currently
	// True on the pod's node (e.g. "MemoryPressure", "DiskPressure",
	// "PIDPressure") — a hint the failure may be node-wide, not this
	// container's fault.
	NodePressureConditions []string
	// NodeNotReady is true when the pod's node's Ready condition isn't True.
	NodeNotReady bool
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

	if len(ev.NodePressureConditions) > 0 {
		res.Summary = fmt.Sprintf("%s Also: the pod's node reports %s.", res.Summary, strings.Join(ev.NodePressureConditions, ", "))
		if res.RootCause == diagv1alpha1.RootCauseOOMKilled && containsString(ev.NodePressureConditions, "MemoryPressure") {
			res.Confidence = diagv1alpha1.ConfidenceHigh
			res.Recommendation = fmt.Sprintf("%s Node-level MemoryPressure detected — raising this pod's own limits may not help; check node allocatable memory and overall workload density first.", res.Recommendation)
		}
	}
	if ev.NodeNotReady {
		res.Summary = fmt.Sprintf("%s Also: the pod's node is currently NotReady.", res.Summary)
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
	case "RunContainerError", "CreateContainerError", "CreateContainerConfigError":
		// The container runtime failed before the process ever ran (e.g. the
		// entrypoint binary doesn't exist, isn't executable, or the config is
		// invalid). There's no real exit code here — runc/containerd report a
		// synthetic one — so this must be caught before the exit-code switch below.
		return Result{
			RootCause:      diagv1alpha1.RootCauseBadCommand,
			Confidence:     diagv1alpha1.ConfidenceHigh,
			Summary:        fmt.Sprintf("Container runtime failed to start the container: %s", firstNonEmpty(ev.WaitingMessage, ev.TerminatedMessage)),
			Recommendation: "Check the container's command/entrypoint against what's actually in the image (missing binary, bad permissions, or invalid working directory).",
		}
	}

	if !ev.HasTerminated {
		return Result{RootCause: diagv1alpha1.RootCauseUnknown}
	}

	if ev.TerminatedReason == "OOMKilled" {
		return Result{
			RootCause:      diagv1alpha1.RootCauseOOMKilled,
			Confidence:     diagv1alpha1.ConfidenceHigh,
			Summary:        "Container exceeded its memory limit and was killed by the kernel OOM killer.",
			Recommendation: "Raise resources.limits.memory, or investigate a possible memory leak in the application.",
		}
	}

	switch ev.TerminatedReason {
	case "StartError", "CreateContainerError", "CreateContainerConfigError":
		// Same synthetic-failure case as the WaitingReason check above, but
		// seen once the container has settled into CrashLoopBackOff and the
		// runtime error moved from Waiting into LastTerminationState.
		return Result{
			RootCause:      diagv1alpha1.RootCauseBadCommand,
			Confidence:     diagv1alpha1.ConfidenceHigh,
			Summary:        fmt.Sprintf("Container runtime failed to start the container: %s", firstNonEmpty(ev.TerminatedMessage, ev.TerminatedReason)),
			Recommendation: "Check the container's command/entrypoint against what's actually in the image (missing binary, bad permissions, or invalid working directory).",
		}
	}

	switch ev.ExitCode {
	case 137:
		conf := diagv1alpha1.ConfidenceMedium
		if strings.Contains(strings.ToLower(ev.LogTail), "out of memory") || strings.Contains(strings.ToLower(ev.LogTail), "oom") {
			conf = diagv1alpha1.ConfidenceHigh
		}
		return Result{
			RootCause:      diagv1alpha1.RootCauseOOMKilled,
			Confidence:     conf,
			Summary:        "Container exited with code 137 (SIGKILL), most commonly caused by an out-of-memory kill.",
			Recommendation: "Check `kubectl describe pod` for an OOMKilled reason and raise resources.limits.memory if confirmed.",
		}
	case 139:
		return Result{
			RootCause:      diagv1alpha1.RootCauseSegFault,
			Confidence:     diagv1alpha1.ConfidenceHigh,
			Summary:        "Container exited with code 139 (SIGSEGV) — a native crash inside the process.",
			Recommendation: "Check for a corrupted binary, incompatible base image (e.g. glibc/musl mismatch), or a bug in native/CGo code.",
		}
	case 143:
		return Result{
			RootCause:      diagv1alpha1.RootCauseSignalKilled,
			Confidence:     diagv1alpha1.ConfidenceMedium,
			Summary:        "Container exited with code 143 (SIGTERM) — it was asked to stop.",
			Recommendation: "Check for node pressure/eviction, a failing liveness probe, or terminationGracePeriodSeconds too short for a clean shutdown.",
		}
	case 126:
		return Result{
			RootCause:      diagv1alpha1.RootCauseBadCommand,
			Confidence:     diagv1alpha1.ConfidenceHigh,
			Summary:        "Container exited with code 126 — the command was found but is not executable.",
			Recommendation: "Check file permissions on the entrypoint script/binary in the image (missing chmod +x).",
		}
	case 127:
		return Result{
			RootCause:      diagv1alpha1.RootCauseBadCommand,
			Confidence:     diagv1alpha1.ConfidenceHigh,
			Summary:        "Container exited with code 127 — the command was not found.",
			Recommendation: "Check the container's command/entrypoint and that the binary exists in the image (common after a slim/distroless base image switch).",
		}
	case 0:
		return Result{RootCause: diagv1alpha1.RootCauseUnknown}
	default:
		return Result{
			RootCause:      diagv1alpha1.RootCauseApplicationError,
			Confidence:     diagv1alpha1.ConfidenceMedium,
			Summary:        fmt.Sprintf("Container process exited with code %d.", ev.ExitCode),
			Recommendation: "Inspect the attached log excerpt for the application-level error that caused the exit.",
		}
	}
}

func probeFailing(events []diagv1alpha1.EvidenceEvent) bool {
	for _, e := range events {
		if e.Reason == "Unhealthy" && strings.Contains(strings.ToLower(e.Message), "probe failed") {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func containsString(vals []string, target string) bool {
	for _, v := range vals {
		if v == target {
			return true
		}
	}
	return false
}
