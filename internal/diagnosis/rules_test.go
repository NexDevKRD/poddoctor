package diagnosis

import (
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
