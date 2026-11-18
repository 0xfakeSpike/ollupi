package sandbox

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

type DomainRules struct {
	Allowed []string
	Denied  []string
}

type domainEntry struct {
	host string
	port int
}

func (e domainEntry) String() string {
	if e.port == 0 {
		return e.host
	}
	return net.JoinHostPort(e.host, strconv.Itoa(e.port))
}

func parseDomainEntry(raw string) (domainEntry, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" || strings.ContainsAny(s, "/@?# \t") || strings.Contains(s, "://") {
		return domainEntry{}, fmt.Errorf("sandbox: domain %q must be a host name, *.domain, *, or an IP address, with an optional :port", raw)
	}
	host, port := s, 0
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			return domainEntry{}, fmt.Errorf("sandbox: domain %q has an invalid port", raw)
		}
		host, port = h, n
	}
	host = strings.Trim(host, "[]")
	if host != "*." {
		host = strings.TrimSuffix(host, ".")
	}
	switch {
	case host == "*":
	case strings.HasPrefix(host, "*."):
		if !validHostName(host[2:]) {
			return domainEntry{}, fmt.Errorf("sandbox: domain %q is not a valid wildcard", raw)
		}
	case net.ParseIP(host) != nil:
	case !validHostName(host) || strings.Contains(host, "*"):
		return domainEntry{}, fmt.Errorf("sandbox: domain %q is not a valid host name", raw)
	}
	return domainEntry{host: host, port: port}, nil
}

func validHostName(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return false
			}
		}
	}
	return true
}

type compiledRules struct {
	allow, deny []domainEntry
}

func compileRules(r DomainRules) (compiledRules, error) {
	allow, err := parseDomainEntries(r.Allowed)
	if err != nil {
		return compiledRules{}, err
	}
	deny, err := parseDomainEntries(r.Denied)
	if err != nil {
		return compiledRules{}, err
	}
	return compiledRules{allow: allow, deny: deny}, nil
}

func (c compiledRules) named() DomainRules {
	out := DomainRules{Allowed: []string{}, Denied: []string{}}
	for _, e := range c.allow {
		out.Allowed = append(out.Allowed, e.String())
	}
	for _, e := range c.deny {
		out.Denied = append(out.Denied, e.String())
	}
	return out
}

func parseDomainEntries(raws []string) ([]domainEntry, error) {
	var out []domainEntry
	for _, raw := range raws {
		e, err := parseDomainEntry(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (e domainEntry) matches(host string, port int) bool {
	if e.port != 0 && e.port != port {
		return false
	}
	switch {
	case e.host == "*":
		return true
	case strings.HasPrefix(e.host, "*."):
		return strings.HasSuffix(host, e.host[1:])
	default:
		return host == e.host
	}
}

func (c compiledRules) decide(host string, port int) (allowed, literal bool) {
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	for _, e := range c.deny {
		if e.matches(host, port) {
			return false, false
		}
	}
	for _, e := range c.allow {
		if e.matches(host, port) {
			allowed = true
			if e.host == host {
				literal = true
			}
		}
	}
	return allowed, literal
}
