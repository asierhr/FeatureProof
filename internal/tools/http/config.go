package httptool

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var sensitiveHeaders = map[string]struct{}{
	"authorization": {}, "cookie": {}, "set-cookie": {}, "proxy-authorization": {},
}

var hopByHopHeaders = map[string]struct{}{
	"connection": {}, "keep-alive": {}, "proxy-authenticate": {}, "proxy-authorization": {},
	"te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {}, "host": {}, "content-length": {},
}

// New validates and snapshots all trusted configuration before returning an executable capability.
func New(config Config) (*Tool, error) {
	scope, err := parseTarget(config.Target.BaseURL)
	if err != nil {
		return nil, &ConfigError{Field: "target.base_url", Cause: err}
	}
	methods, err := normalizeMethods(config.AllowedMethods, false)
	if err != nil {
		return nil, &ConfigError{Field: "allowed_methods", Cause: err}
	}
	redirectMethods, err := normalizeMethods(config.FollowRedirectsForMethods, true)
	if err != nil {
		return nil, &ConfigError{Field: "follow_redirects_for_methods", Cause: err}
	}
	for method := range redirectMethods {
		if _, ok := methods[method]; !ok {
			return nil, &ConfigError{Field: "follow_redirects_for_methods", Cause: fmt.Errorf("redirect method is not allowed")}
		}
	}
	requestHeaders, err := normalizeHeaders(config.AllowedRequestHeaders, true)
	if err != nil {
		return nil, &ConfigError{Field: "allowed_request_headers", Cause: err}
	}
	responseHeaders, err := normalizeHeaders(config.AllowedResponseHeaders, false)
	if err != nil {
		return nil, &ConfigError{Field: "allowed_response_headers", Cause: err}
	}
	if config.MaxRedirects < 0 {
		return nil, &ConfigError{Field: "max_redirects"}
	}
	if config.Timeout <= 0 {
		return nil, &ConfigError{Field: "timeout"}
	}
	limits := []struct {
		field string
		value int
	}{{"max_url_bytes", config.MaxURLBytes}, {"max_request_header_bytes", config.MaxRequestHeaderBytes}, {"max_request_body_bytes", config.MaxRequestBodyBytes}, {"max_response_header_bytes", config.MaxResponseHeaderBytes}, {"max_response_body_bytes", config.MaxResponseBodyBytes}}
	for _, limit := range limits {
		if limit.value <= 0 {
			return nil, &ConfigError{Field: limit.field}
		}
	}
	if config.Resolver == nil || config.Dialer == nil || config.Clock == nil || config.WithTimeout == nil {
		return nil, &ConfigError{Field: "dependencies", Cause: fmt.Errorf("resolver, dialer, clock, and timeout factory are required")}
	}
	return &Tool{
		scope: scope, methods: methods, requestHeaders: requestHeaders, responseHeaders: responseHeaders,
		redirectMethods: redirectMethods, maxRedirects: config.MaxRedirects, timeout: config.Timeout,
		maxURL: config.MaxURLBytes, maxRequestHeader: config.MaxRequestHeaderBytes,
		maxRequestBody: config.MaxRequestBodyBytes, maxResponseHead: config.MaxResponseHeaderBytes,
		maxResponseBody: config.MaxResponseBodyBytes, resolver: config.Resolver, dialer: config.Dialer,
		clock: config.Clock, withTimeout: config.WithTimeout,
	}, nil
}

// parseTarget converts a trusted base URL into exact scheme, authority, port, and decoded path segments.
func parseTarget(raw string) (targetScope, error) {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return targetScope{}, fmt.Errorf("absolute target is required")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return targetScope{}, fmt.Errorf("unsupported scheme")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return targetScope{}, fmt.Errorf("userinfo, query, and fragment are forbidden")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return targetScope{}, fmt.Errorf("hostname is required")
	}
	for i := 0; i < len(host); i++ {
		if host[i] > 0x7f || host[i] <= 0x20 {
			return targetScope{}, fmt.Errorf("hostname must be normalized ASCII")
		}
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return targetScope{}, fmt.Errorf("invalid port")
	}
	segments, err := parsePath(u.EscapedPath(), true)
	if err != nil {
		return targetScope{}, fmt.Errorf("invalid base path: %w", err)
	}
	return targetScope{scheme: u.Scheme, hostname: host, port: port, baseSegments: segments}, nil
}

// normalizeMethods copies a method allowlist and rejects ambiguous or malformed permissions.
func normalizeMethods(values []string, allowEmpty bool) (map[string]struct{}, error) {
	if values == nil || (!allowEmpty && len(values) == 0) {
		return nil, fmt.Errorf("explicit method list is required")
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || value != strings.ToUpper(value) || !validToken(value) {
			return nil, fmt.Errorf("invalid method")
		}
		if _, exists := out[value]; exists {
			return nil, fmt.Errorf("duplicate method")
		}
		out[value] = struct{}{}
	}
	return out, nil
}

// normalizeHeaders snapshots a canonical header allowlist while excluding framing and secret-bearing names.
func normalizeHeaders(values []string, request bool) (map[string]string, error) {
	if values == nil {
		return nil, fmt.Errorf("explicit header list is required")
	}
	out := make(map[string]string, len(values))
	for _, value := range values {
		if !validToken(value) {
			return nil, fmt.Errorf("invalid header name")
		}
		lower := strings.ToLower(value)
		if _, duplicate := out[lower]; duplicate {
			return nil, fmt.Errorf("duplicate header")
		}
		if _, secret := sensitiveHeaders[lower]; secret {
			return nil, fmt.Errorf("sensitive header is forbidden")
		}
		if request {
			if _, hop := hopByHopHeaders[lower]; hop {
				return nil, fmt.Errorf("hop-by-hop header is forbidden")
			}
		}
		out[lower] = http.CanonicalHeaderKey(value)
	}
	return out, nil
}

// validToken implements the ASCII token subset accepted for HTTP methods and header names.
func validToken(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c))) {
			return false
		}
	}
	return true
}

// authority returns the exact hostname and effective port authorized for HTTP request URLs.
func (s targetScope) authority() string {
	if (s.scheme == "http" && s.port == "80") || (s.scheme == "https" && s.port == "443") {
		if strings.Contains(s.hostname, ":") {
			return "[" + s.hostname + "]"
		}
		return s.hostname
	}
	return net.JoinHostPort(s.hostname, s.port)
}
