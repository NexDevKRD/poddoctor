// Package severity maps a root cause to a coarse severity bucket, shared
// by every dashboard (per-cluster and fleet hub) so the classification
// lives in exactly one place.
package severity

// Of returns "critical", "high", "medium", or "unknown" for rootCause.
func Of(rootCause string) string {
	switch rootCause {
	case "OOMKilled", "SegFault":
		return "critical"
	case "ImagePullError", "BadCommand", "ProbeFailure":
		return "high"
	case "SignalKilled", "RecentRolloutRegression", "ApplicationError":
		return "medium"
	default:
		return "unknown"
	}
}
