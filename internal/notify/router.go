package notify

import (
	"os"

	"sigs.k8s.io/yaml"
)

// Route sends diagnoses from any namespace in Namespaces (or "*" for
// catch-all) to WebhookURL. Routes are checked in order; first match wins.
type Route struct {
	Namespaces []string `json:"namespaces"`
	WebhookURL string   `json:"webhookURL"`
	Format     Format   `json:"webhookFormat"`
	Token      string   `json:"webhookToken,omitempty"`
}

// Config is the on-disk shape loaded via --notify-config, for routing
// different namespaces (e.g. different teams) to different webhooks.
type Config struct {
	DefaultWebhookURL string  `json:"defaultWebhookURL"`
	DefaultFormat     Format  `json:"defaultWebhookFormat"`
	DefaultToken      string  `json:"defaultWebhookToken,omitempty"`
	Routes            []Route `json:"routes"`
}

// LoadConfig reads and parses a --notify-config file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Router picks which webhook (if any) a namespace's diagnoses go to.
type Router struct {
	routes         []Route
	fallbackURL    string
	fallbackFormat Format
	fallbackToken  string
}

// NewRouter builds a Router. fallbackURL/fallbackFormat/fallbackToken are
// used when no route matches (or when routes is empty, making this a
// single-webhook router).
func NewRouter(fallbackURL string, fallbackFormat Format, fallbackToken string, routes []Route) *Router {
	return &Router{routes: routes, fallbackURL: fallbackURL, fallbackFormat: fallbackFormat, fallbackToken: fallbackToken}
}

// Route returns the webhook target for namespace, and whether one is
// configured at all.
func (r *Router) Route(namespace string) (url string, format Format, token string, ok bool) {
	if r == nil {
		return "", "", "", false
	}
	for _, route := range r.routes {
		for _, ns := range route.Namespaces {
			if ns == "*" || ns == namespace {
				return route.WebhookURL, route.Format, route.Token, route.WebhookURL != ""
			}
		}
	}
	if r.fallbackURL != "" {
		return r.fallbackURL, r.fallbackFormat, r.fallbackToken, true
	}
	return "", "", "", false
}
