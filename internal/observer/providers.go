package observer

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Provider is an AI provider whose API traffic the observer recognizes, by exact hostname.
// To add one, append to Providers with the hostnames its API documentation names. Suffix
// matching is deliberately not supported: list each documented API subdomain.
type Provider struct {
	ID    string
	Name  string
	Hosts []string
	Docs  string // where the hostnames come from
}

// Providers is the table of known AI providers.
var Providers = []Provider{
	{ID: "openai", Name: "OpenAI", Hosts: []string{"api.openai.com",
		// Data residency (regional processing or storage) API hosts.
		"us.api.openai.com", "eu.api.openai.com", "au.api.openai.com", "jp.api.openai.com", "in.api.openai.com",
		"ae.api.openai.com", "ca.api.openai.com", "sg.api.openai.com", "kr.api.openai.com", "gb.api.openai.com"},
		Docs: "https://developers.openai.com/api/docs/guides/data-residency"},
	{ID: "anthropic", Name: "Anthropic", Hosts: []string{"api.anthropic.com"}, Docs: "https://platform.claude.com/docs/en/api/overview"},
	{ID: "openrouter", Name: "OpenRouter", Hosts: []string{"openrouter.ai", "eu.openrouter.ai", "us.openrouter.ai"},
		Docs: "https://openrouter.ai/docs/api/reference/overview ; https://openrouter.ai/docs/guides/features/in-region-routing"},
}

// providerHost maps a lowercase hostname (without a trailing dot) to its provider.
func providerHost(providers []Provider, host string) (Provider, bool) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for _, p := range providers {
		for _, h := range p.Hosts {
			if h == host {
				return p, true
			}
		}
	}
	return Provider{}, false
}

// IsProviderHost reports whether host is a known provider hostname.
func IsProviderHost(host string) bool {
	_, ok := providerHost(Providers, host)
	return ok
}

// Resolver looks up a hostname's addresses (net.DefaultResolver in production).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Match is what an IP address resolved from.
type Match struct {
	Provider Provider
	Host     string
}

// ResolutionCache keeps the addresses of every provider hostname, refreshed after ttl. A
// failed refresh keeps the previous answer until maxStale, after which it is purged, so a
// long network outage does not keep matching addresses that may have moved.
type ResolutionCache struct {
	providers []Provider
	resolver  Resolver
	ttl       time.Duration
	maxStale  time.Duration
	now       func() time.Time

	mu      sync.RWMutex
	entries map[string]resolution // host → answer
	byIP    map[netip.Addr]Match
}

type resolution struct {
	addrs    []netip.Addr
	fetched  time.Time
	attempts time.Time
}

// NewResolutionCache returns an empty cache for providers.
func NewResolutionCache(providers []Provider, resolver Resolver, ttl, maxStale time.Duration) *ResolutionCache {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &ResolutionCache{providers: providers, resolver: resolver, ttl: ttl, maxStale: maxStale, now: time.Now, entries: map[string]resolution{}, byIP: map[netip.Addr]Match{}}
}

// Refresh re-resolves the hostnames whose answers are older than the TTL and purges answers
// older than maxStale. It is cheap to call often.
func (c *ResolutionCache) Refresh(ctx context.Context) {
	now := c.now()
	changed := false
	for _, p := range c.providers {
		for _, host := range p.Hosts {
			c.mu.RLock()
			entry, ok := c.entries[host]
			c.mu.RUnlock()
			if ok && now.Sub(entry.fetched) < c.ttl {
				continue
			}
			if ok && now.Sub(entry.attempts) < c.ttl/4 {
				continue // a recent attempt failed; do not hammer the resolver
			}
			lookup, cancel := context.WithTimeout(ctx, 3*time.Second)
			addrs, err := c.resolver.LookupNetIP(lookup, "ip", host)
			cancel()
			c.mu.Lock()
			entry.attempts = now
			if err == nil && len(addrs) > 0 {
				for i := range addrs {
					addrs[i] = addrs[i].Unmap()
				}
				entry.addrs, entry.fetched = addrs, now
				changed = true
			} else if ok && now.Sub(entry.fetched) > c.maxStale {
				entry.addrs = nil
				changed = true
			}
			c.entries[host] = entry
			c.mu.Unlock()
		}
	}
	if changed {
		c.rebuild()
	}
}

// Learn records an answer observed elsewhere (for example a DNS log), as if resolved now.
func (c *ResolutionCache) Learn(host string, addrs []netip.Addr) {
	p, ok := providerHost(c.providers, host)
	if !ok || len(addrs) == 0 {
		return
	}
	_ = p
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	c.mu.Lock()
	entry := c.entries[host]
	seen := map[netip.Addr]bool{}
	for _, a := range entry.addrs {
		seen[a] = true
	}
	for _, a := range addrs {
		if a = a.Unmap(); !seen[a] {
			entry.addrs = append(entry.addrs, a)
			seen[a] = true
		}
	}
	entry.fetched = c.now()
	c.entries[host] = entry
	c.mu.Unlock()
	c.rebuild()
}

func (c *ResolutionCache) rebuild() {
	c.mu.Lock()
	defer c.mu.Unlock()
	byIP := make(map[netip.Addr]Match, len(c.byIP))
	for _, p := range c.providers {
		for _, host := range p.Hosts {
			for _, a := range c.entries[host].addrs {
				byIP[a] = Match{Provider: p, Host: host}
			}
		}
	}
	c.byIP = byIP
}

// Lookup returns the provider an address belongs to.
func (c *ResolutionCache) Lookup(addr netip.Addr) (Match, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.byIP[addr.Unmap()]
	return m, ok
}

// Size is how many addresses are cached.
func (c *ResolutionCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byIP)
}

// HostsIn returns the provider hostnames that occur in text as whole names (delimited by
// characters that cannot be part of a hostname), for sources that only offer log lines.
func HostsIn(providers []Provider, text string) []string {
	lower := strings.ToLower(text)
	var out []string
	for _, p := range providers {
		for _, h := range p.Hosts {
			for from := 0; ; {
				i := strings.Index(lower[from:], h)
				if i < 0 {
					break
				}
				start, end := from+i, from+i+len(h)
				if (start == 0 || !hostChar(lower[start-1])) && (end == len(lower) || !hostChar(lower[end]) || (lower[end] == '.' && (end+1 == len(lower) || !hostChar(lower[end+1])))) {
					out = append(out, h)
					break
				}
				from = end
			}
		}
	}
	return out
}

func hostChar(b byte) bool {
	return b == '.' || b == '-' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}
