// Package notify sends a best-effort outbound alert when PodDoctor
// diagnoses a failure, so someone doesn't need to go look for it.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	diagv1alpha1 "github.com/chenar/poddoctor/api/v1alpha1"
)

// Format selects the outbound JSON payload shape.
type Format string

const (
	FormatGeneric Format = "generic"
	FormatSlack   Format = "slack"
)

// Payload is the generic (non-Slack) webhook body. It's also what the
// fleet hub (cmd/hub) expects on its ingest endpoint, so a cluster is
// just another webhook target.
type Payload struct {
	Cluster         string `json:"cluster,omitempty"`
	Pod             string `json:"pod"`
	Namespace       string `json:"namespace"`
	Container       string `json:"container"`
	RootCause       string `json:"rootCause"`
	Confidence      string `json:"confidence"`
	Summary         string `json:"summary"`
	Recommendation  string `json:"recommendation"`
	SuppressedCount int    `json:"suppressedCount,omitempty"`
}

// Notification is one outbound alert: a diagnosis, optionally tagged with
// which cluster it came from and how many identical diagnoses (same
// namespace + root cause) were folded into it by alert grouping.
type Notification struct {
	Diag            *diagv1alpha1.PodDiagnosis
	Cluster         string
	SuppressedCount int
}

// Send posts n to url in the given format. If token is non-empty, it's
// sent as a Bearer Authorization header (e.g. for the fleet hub's ingest
// endpoint). ctx should carry a deadline; Send does not add one of its own.
func Send(ctx context.Context, url string, format Format, token string, n Notification) error {
	diag := n.Diag
	p := Payload{
		Cluster:         n.Cluster,
		Pod:             diag.Spec.PodName,
		Namespace:       diag.Spec.PodNamespace,
		Container:       diag.Spec.ContainerName,
		RootCause:       string(diag.Status.RootCause),
		Confidence:      string(diag.Status.Confidence),
		Summary:         diag.Status.Summary,
		Recommendation:  diag.Status.Recommendation,
		SuppressedCount: n.SuppressedCount,
	}

	var body []byte
	var err error
	if format == FormatSlack {
		text := fmt.Sprintf("*PodDoctor*: `%s/%s` (%s) — %s/%s\n%s\n_Recommendation:_ %s",
			p.Namespace, p.Pod, p.Container, p.RootCause, p.Confidence, p.Summary, p.Recommendation)
		if p.SuppressedCount > 0 {
			text += fmt.Sprintf("\n_(+%d more like this since the last alert)_", p.SuppressedCount)
		}
		body, err = json.Marshal(map[string]string{"text": text})
	} else {
		body, err = json.Marshal(p)
	}
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}
