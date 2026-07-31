package allowlist

import (
	"fmt"
	"net"
	"strings"
)

// Rule represents a single allow rule parsed from a pattern string.
type Rule struct {
	host       string // lowercase hostname or wildcard (e.g., "*.corp.local")
	port       string // specific port or empty for "all ports"
	isWildcard bool   // true if host starts with "*."
}

// Allowlist validates target hostnames against configured allow rules.
type Allowlist struct {
	rules []Rule
}

// Parse creates an Allowlist from a list of pattern strings.
func Parse(patterns []string) (*Allowlist, error) {
	if len(patterns) == 0 {
		return nil, fmt.Errorf("at least one allow pattern is required")
	}

	rules := make([]Rule, 0, len(patterns))
	for _, p := range patterns {
		rule, err := parseRule(p)
		if err != nil {
			return nil, fmt.Errorf("invalid allow pattern %q: %w", p, err)
		}
		rules = append(rules, rule)
	}

	return &Allowlist{rules: rules}, nil
}

func parseRule(pattern string) (Rule, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return Rule{}, fmt.Errorf("empty pattern")
	}

	// Reject global wildcards
	if pattern == "*" || pattern == "*.*" || pattern == "*:*" {
		return Rule{}, fmt.Errorf("global wildcards are not allowed")
	}

	var host, port string

	// Check if there's a port specified
	if h, p, err := net.SplitHostPort(pattern); err == nil {
		host = h
		port = p
	} else {
		host = pattern
	}

	host = strings.ToLower(host)

	isWildcard := strings.HasPrefix(host, "*.")
	if isWildcard {
		// Validate the wildcard domain has at least a TLD
		suffix := host[2:] // strip "*."
		if !strings.Contains(suffix, ".") {
			return Rule{}, fmt.Errorf("wildcard must include at least a domain and TLD (e.g., *.corp.local), got %q", pattern)
		}
	} else if strings.Contains(host, "*") {
		return Rule{}, fmt.Errorf("wildcards are only supported as a prefix (*.domain), got %q", pattern)
	}

	return Rule{
		host:       host,
		port:       port,
		isWildcard: isWildcard,
	}, nil
}

// IsAllowed checks whether a given host:port is permitted by the allowlist.
func (a *Allowlist) IsAllowed(host, port string) bool {
	host = strings.ToLower(host)

	for _, rule := range a.rules {
		if !a.matchHost(rule, host) {
			continue
		}
		if rule.port != "" && rule.port != port {
			continue
		}
		return true
	}
	return false
}

func (a *Allowlist) matchHost(rule Rule, host string) bool {
	if rule.isWildcard {
		suffix := rule.host[1:] // "*." → ".corp.local"
		return strings.HasSuffix(host, suffix)
	}
	return rule.host == host
}
