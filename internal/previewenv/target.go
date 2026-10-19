package previewenv

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// NetworkPolicy is one trusted exact target allowlist selected by catalog ID.
type NetworkPolicy struct {
	ID             string
	Schemes, Hosts []string
	Ports          []int
	BasePaths      []string
	MaxURLBytes    int
}

// AuthorizedTarget is the immutable canonical target handed to execution composition.
type AuthorizedTarget struct {
	BaseURL, PolicyID, Host string
	Port                    int
	BasePath                string
}

// AuthorizeTarget parses and authorizes a candidate without DNS lookup or socket access.
func AuthorizeTarget(candidate string, policy NetworkPolicy) (AuthorizedTarget, error) {
	if policy.MaxURLBytes <= 0 || len(candidate) > policy.MaxURLBytes || !safeID.MatchString(policy.ID) {
		return AuthorizedTarget{}, &Error{Code: CodeUnauthorized, Operation: "target"}
	}
	u, err := url.Parse(candidate)
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return AuthorizedTarget{}, &Error{Code: CodeUnauthorized, Operation: "target"}
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" || !contains(policy.Schemes, u.Scheme) || !contains(policy.Hosts, host) {
		return AuthorizedTarget{}, &Error{Code: CodeUnauthorized, Operation: "target"}
	}
	port := 80
	if u.Scheme == "https" {
		port = 443
	}
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			return AuthorizedTarget{}, &Error{Code: CodeUnauthorized, Operation: "target"}
		}
	}
	if !containsInt(policy.Ports, port) {
		return AuthorizedTarget{}, &Error{Code: CodeUnauthorized, Operation: "target"}
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if !contains(policy.BasePaths, path) || strings.Contains(path, "..") || strings.Contains(strings.ToLower(path), "%2f") {
		return AuthorizedTarget{}, &Error{Code: CodeUnauthorized, Operation: "target"}
	}
	authority := host
	if port != 80 && port != 443 {
		authority = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return AuthorizedTarget{BaseURL: u.Scheme + "://" + authority + path, PolicyID: policy.ID, Host: host, Port: port, BasePath: path}, nil
}

// containsInt performs exact effective-port authorization.
func containsInt(values []int, wanted int) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
