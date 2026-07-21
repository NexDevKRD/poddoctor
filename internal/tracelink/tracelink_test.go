package tracelink

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestURL_DisabledWithoutBothFields(t *testing.T) {
	cases := []Config{
		{},
		{GrafanaURL: "https://grafana.example.com"},
		{TempoDatasourceUID: "abc123"},
	}
	for _, c := range cases {
		if got := c.URL("default", "demo-oomkilled"); got != "" {
			t.Errorf("Config %+v: URL() = %q, want empty", c, got)
		}
	}
}

func TestURL_BuildsGrafanaExploreLink(t *testing.T) {
	c := Config{GrafanaURL: "https://grafana.example.com/", TempoDatasourceUID: "tempo-uid-1"}
	got := c.URL("payments", "payments-api-abc")

	if !strings.HasPrefix(got, "https://grafana.example.com/explore?left=") {
		t.Fatalf("unexpected URL: %s", got)
	}

	rawLeft, err := url.QueryUnescape(strings.TrimPrefix(got, "https://grafana.example.com/explore?left="))
	if err != nil {
		t.Fatalf("unescape: %v", err)
	}
	var left map[string]any
	if err := json.Unmarshal([]byte(rawLeft), &left); err != nil {
		t.Fatalf("decode left param: %v (raw=%s)", err, rawLeft)
	}
	if left["datasource"] != "tempo-uid-1" {
		t.Fatalf("datasource = %v, want tempo-uid-1", left["datasource"])
	}
	queries, ok := left["queries"].([]any)
	if !ok || len(queries) != 1 {
		t.Fatalf("expected 1 query, got %+v", left["queries"])
	}
	query := queries[0].(map[string]any)["query"].(string)
	if !strings.Contains(query, `k8s.namespace.name="payments"`) || !strings.Contains(query, `k8s.pod.name="payments-api-abc"`) {
		t.Fatalf("unexpected TraceQL query: %s", query)
	}
}
