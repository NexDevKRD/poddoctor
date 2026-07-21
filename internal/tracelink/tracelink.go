// Package tracelink builds Grafana Explore deep links into Tempo traces
// for a given pod/namespace. PodDoctor doesn't collect, store, or render
// traces itself — Tempo and Grafana already do that well; this just
// points the dashboard at them.
package tracelink

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Config describes how to reach Grafana/Tempo. Both fields are required
// for links to be generated — Grafana's Explore URL format encodes the
// datasource by UID, which varies per install and can't be guessed.
type Config struct {
	GrafanaURL         string
	TempoDatasourceUID string
}

// Enabled reports whether enough is configured to build links.
func (c Config) Enabled() bool {
	return c.GrafanaURL != "" && c.TempoDatasourceUID != ""
}

// URL returns a Grafana Explore deep link running a TraceQL query for
// traces from this pod/namespace over the last hour, or "" if tracing
// isn't configured. Relies on the standard OpenTelemetry resource
// attributes (k8s.namespace.name, k8s.pod.name) — only traces from
// OTel-instrumented applications carry these, PodDoctor doesn't add them.
func (c Config) URL(namespace, pod string) string {
	if !c.Enabled() {
		return ""
	}

	query := fmt.Sprintf(`{resource.k8s.namespace.name="%s" && resource.k8s.pod.name="%s"}`, namespace, pod)
	left, err := json.Marshal(map[string]any{
		"datasource": c.TempoDatasourceUID,
		"queries": []map[string]any{{
			"datasource": map[string]string{"type": "tempo", "uid": c.TempoDatasourceUID},
			"queryType":  "traceql",
			"query":      query,
		}},
		"range": map[string]string{"from": "now-1h", "to": "now"},
	})
	if err != nil {
		return ""
	}

	return fmt.Sprintf("%s/explore?left=%s", strings.TrimRight(c.GrafanaURL, "/"), url.QueryEscape(string(left)))
}
