package app

import (
	"errors"
	"net/http"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/weburl"
)

// RequestTimeout bounds every secure request, body included.
const RequestTimeout = 60 * time.Second

// ErrRedirect is returned for a redirect: like fetch with redirect: 'error', a secure client
// never follows one.
var ErrRedirect = errors.New("redirect refused")

type secureTransport struct {
	allowlist contracts.CliHostAllowlist
	base      http.RoundTripper
}

// checkDestination refuses a URL that is not a safe CLI URL (ignoring its query) or whose
// host is not on the allowlist, before anything is sent.
func checkDestination(href string, allowlist contracts.CliHostAllowlist) error {
	u, err := weburl.Parse(href)
	if err != nil {
		return err
	}
	base := u.Clone()
	base.SetSearch("")
	if !contracts.SafeCliURL(base.Href(), allowlist.Development) {
		return clierror.New("Unsafe authorization or MCP destination.")
	}
	unlisted, err := contracts.UnlistedCliHost(u.Href(), allowlist)
	if err != nil {
		return err
	}
	if unlisted != "" {
		return clierror.New(unlisted)
	}
	return nil
}

func (t *secureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := checkDestination(req.URL.String(), t.allowlist); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	return t.base.RoundTrip(req)
}

// SecureFetch is secureFetch (src/safe-fetch.ts) as an HTTP client: every request, wherever
// metadata or a server sent it, must stay on allowlist; redirects are refused; each request
// times out after RequestTimeout. Its errors wrap the clierror for a refused destination.
// base may be nil (http.DefaultClient).
func SecureFetch(allowlist contracts.CliHostAllowlist, base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client := *base
	client.Transport = &secureTransport{allowlist: allowlist, base: transport}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return ErrRedirect }
	client.Timeout = RequestTimeout
	return &client
}
