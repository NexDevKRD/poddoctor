package diagnosis

import (
	"strings"
	"testing"

	diagv1alpha1 "github.com/chenar/poddoctor/api/v1alpha1"
)

func TestDiagnose(t *testing.T) {
	cases := []struct {
		name string
		ev   Evidence
		want diagv1alpha1.RootCause
	}{
		{
			name: "image pull backoff",
			ev:   Evidence{WaitingReason: "ImagePullBackOff", WaitingMessage: "manifest not found"},
			want: diagv1alpha1.RootCauseImagePullError,
		},
		{
			name: "err image pull",
			ev:   Evidence{WaitingReason: "ErrImagePull"},
			want: diagv1alpha1.RootCauseImagePullError,
		},
		{
			name: "oom killed via reason",
			ev:   Evidence{HasTerminated: true, TerminatedReason: "OOMKilled", ExitCode: 137},
			want: diagv1alpha1.RootCauseOOMKilled,
		},
		{
			name: "exit 137 without explicit reason",
			ev:   Evidence{HasTerminated: true, TerminatedReason: "Error", ExitCode: 137},
			want: diagv1alpha1.RootCauseOOMKilled,
		},
		{
			name: "segfault",
			ev:   Evidence{HasTerminated: true, ExitCode: 139},
			want: diagv1alpha1.RootCauseSegFault,
		},
		{
			name: "sigterm",
			ev:   Evidence{HasTerminated: true, ExitCode: 143},
			want: diagv1alpha1.RootCauseSignalKilled,
		},
		{
			name: "bad command not executable",
			ev:   Evidence{HasTerminated: true, ExitCode: 126},
			want: diagv1alpha1.RootCauseBadCommand,
		},
		{
			name: "command not found",
			ev:   Evidence{HasTerminated: true, ExitCode: 127},
			want: diagv1alpha1.RootCauseBadCommand,
		},
		{
			name: "runtime start error via waiting reason (first restart)",
			ev:   Evidence{WaitingReason: "RunContainerError", WaitingMessage: `exec: "/bin/does-not-exist": stat /bin/does-not-exist: no such file or directory`},
			want: diagv1alpha1.RootCauseBadCommand,
		},
		{
			name: "runtime start error via termination reason (steady-state CrashLoopBackOff)",
			ev: Evidence{
				WaitingReason:     "CrashLoopBackOff",
				HasTerminated:     true,
				TerminatedReason:  "StartError",
				TerminatedMessage: `exec: "/bin/does-not-exist": stat /bin/does-not-exist: no such file or directory`,
				ExitCode:          128,
			},
			want: diagv1alpha1.RootCauseBadCommand,
		},
		{
			name: "generic application error",
			ev:   Evidence{HasTerminated: true, ExitCode: 1},
			want: diagv1alpha1.RootCauseApplicationError,
		},
		{
			name: "no signal at all",
			ev:   Evidence{},
			want: diagv1alpha1.RootCauseUnknown,
		},
		{
			name: "unknown but recent rollout becomes the lead",
			ev:   Evidence{RecentRollout: true, RolloutContext: "started 45s after deployment/api rolled to revision 12"},
			want: diagv1alpha1.RootCauseRecentRollout,
		},
		{
			name: "probe failure overrides sigterm",
			ev: Evidence{
				HasTerminated: true,
				ExitCode:      143,
				RecentEvents: []diagv1alpha1.EvidenceEvent{
					{Reason: "Unhealthy", Message: "Liveness probe failed: HTTP probe failed with statuscode: 500"},
				},
			},
			want: diagv1alpha1.RootCauseProbeFailure,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Diagnose(tc.ev)
			if got.RootCause != tc.want {
				t.Fatalf("RootCause = %s, want %s (summary=%q)", got.RootCause, tc.want, got.Summary)
			}
			if got.Summary == "" {
				t.Fatalf("expected non-empty summary")
			}
			if got.Confidence == "" {
				t.Fatalf("expected confidence to always be set")
			}
		})
	}
}

func TestDiagnose_NodeMemoryPressureUpgradesOOMKilledConfidence(t *testing.T) {
	ev := Evidence{
		HasTerminated:          true,
		TerminatedReason:       "OOMKilled",
		ExitCode:               137,
		NodePressureConditions: []string{"MemoryPressure"},
	}
	got := Diagnose(ev)
	if got.RootCause != diagv1alpha1.RootCauseOOMKilled {
		t.Fatalf("RootCause = %s, want OOMKilled", got.RootCause)
	}
	if got.Confidence != diagv1alpha1.ConfidenceHigh {
		t.Fatalf("Confidence = %s, want High when node reports MemoryPressure", got.Confidence)
	}
	if !strings.Contains(got.Summary, "MemoryPressure") {
		t.Fatalf("expected summary to mention MemoryPressure, got %q", got.Summary)
	}
}

func TestDiagnose_NodeNotReadyNotedInSummary(t *testing.T) {
	ev := Evidence{HasTerminated: true, ExitCode: 1, NodeNotReady: true}
	got := Diagnose(ev)
	if !strings.Contains(got.Summary, "NotReady") {
		t.Fatalf("expected summary to mention NotReady, got %q", got.Summary)
	}
}
