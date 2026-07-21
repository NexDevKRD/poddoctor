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

// Payload is the generic (non-Slack) webhook body.
type Payload struct {
	Pod            string `json:"pod"`
	Namespace      string `json:"namespace"`
	Container      string `json:"container"`
	RootCause      string `json:"rootCause"`
	Confidence     string `json:"confidence"`
	Summary        string `json:"summary"`
	Recommendation string `json:"recommendation"`
}

// Send posts diag to url in the given format. ctx should carry a deadline;
// Send does not add one of its own.
func Send(ctx context.Context, url string, format Format, diag *diagv1alpha1.PodDiagnosis) error {
	p := Payload{
		Pod:            diag.Spec.PodName,
		Namespace:      diag.Spec.PodNamespace,
		Container:      diag.Spec.ContainerName,
		RootCause:      string(diag.Status.RootCause),
		Confidence:     string(diag.Status.Confidence),
		Summary:        diag.Status.Summary,
		Recommendation: diag.Status.Recommendation,
	}

	var body []byte
	var err error
	if format == FormatSlack {
		text := fmt.Sprintf("*PodDoctor*: `%s/%s` (%s) — %s/%s\n%s\n_Recommendation:_ %s",
			p.Namespace, p.Pod, p.Container, p.RootCause, p.Confidence, p.Summary, p.Recommendation)
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
