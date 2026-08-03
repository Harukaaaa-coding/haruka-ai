package voice

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"GopherAI/config"
)

// originAllowed is deliberately stricter than the legacy SSE endpoint.
// Browser WebSocket upgrades are GET requests and therefore do not pass the
// normal cookie-CSRF middleware; Origin plus the first start-frame CSRF token
// form the two browser-facing controls for this endpoint.
func originAllowed(request *http.Request) bool {
	if request == nil {
		return false
	}
	rawOrigin := strings.TrimSpace(request.Header.Get("Origin"))
	if rawOrigin == "" {
		// Native clients do not send Origin. Browser clients always do, and a
		// production deployment should not silently accept an absent one.
		return !config.IsProduction()
	}
	origin, err := url.Parse(rawOrigin)
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	if origin.Scheme != "http" && origin.Scheme != "https" {
		return false
	}
	if config.IsProduction() && origin.Scheme != "https" {
		return false
	}

	if originMatchesConfigured(origin) {
		return true
	}
	if strings.EqualFold(origin.Host, request.Host) {
		return true
	}
	// The Vue development proxy has a different local port from the backend.
	// Keep that convenience local-only; never extend it to production.
	return !config.IsProduction() && isLoopbackHost(origin.Hostname())
}

func originMatchesConfigured(origin *url.URL) bool {
	if origin == nil {
		return false
	}
	conf := config.GetConfig()
	if conf == nil {
		return false
	}
	for _, rawAllowed := range strings.Split(conf.VoiceRealtimeConfig.AllowedOrigins, ",") {
		allowed := strings.TrimSpace(rawAllowed)
		if allowed == "" {
			continue
		}
		parsed, err := url.Parse(allowed)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			continue
		}
		if strings.EqualFold(parsed.Scheme, origin.Scheme) && strings.EqualFold(parsed.Host, origin.Host) {
			return true
		}
	}
	return false
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}
