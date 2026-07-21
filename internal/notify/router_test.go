package notify

import "testing"

func TestRouter_FallbackOnly(t *testing.T) {
	r := NewRouter("https://default.example/hook", FormatSlack, "tok", nil)

	url, format, token, ok := r.Route("payments")
	if !ok || url != "https://default.example/hook" || format != FormatSlack || token != "tok" {
		t.Fatalf("got url=%q format=%q token=%q ok=%v", url, format, token, ok)
	}
}

func TestRouter_NoFallbackNoRoutes(t *testing.T) {
	r := NewRouter("", "", "", nil)
	if _, _, _, ok := r.Route("payments"); ok {
		t.Fatalf("expected no route when nothing configured")
	}
}

func TestRouter_ExactNamespaceMatchWinsOverCatchAll(t *testing.T) {
	routes := []Route{
		{Namespaces: []string{"payments", "checkout"}, WebhookURL: "https://team.example/hook", Format: FormatSlack},
		{Namespaces: []string{"*"}, WebhookURL: "https://platform.example/hook", Format: FormatGeneric},
	}
	r := NewRouter("", "", "", routes)

	url, format, _, ok := r.Route("payments")
	if !ok || url != "https://team.example/hook" || format != FormatSlack {
		t.Fatalf("expected payments to match team route, got url=%q format=%q ok=%v", url, format, ok)
	}

	url, format, _, ok = r.Route("some-other-ns")
	if !ok || url != "https://platform.example/hook" || format != FormatGeneric {
		t.Fatalf("expected unmatched namespace to hit catch-all, got url=%q format=%q ok=%v", url, format, ok)
	}
}

func TestRouter_NilRouterHasNoRoute(t *testing.T) {
	var r *Router
	if _, _, _, ok := r.Route("anything"); ok {
		t.Fatalf("expected nil router to never route")
	}
}
