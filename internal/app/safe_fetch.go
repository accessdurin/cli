package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/mcp/httpx"
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

// HTTPFetch adapts client to the MCP SDK port's fetch. The client's own redirect policy and
// timeout apply whatever redirect mode a call asks for: a SecureFetch client refuses every
// redirect, as secureFetch forces redirect: 'error'. A destination SecureFetch refuses comes
// back as its expected error, as secureFetch throws a CliError before fetching; any other
// failure to get a response is a TypeError, as fetch reports it.
func HTTPFetch(client *http.Client) httpx.Fetch {
	return func(ctx context.Context, url string, init *httpx.RequestInit) (*http.Response, error) {
		if init == nil {
			init = &httpx.RequestInit{}
		}
		method := init.Method
		if method == "" {
			method = http.MethodGet
		}
		var body io.Reader
		if init.Body != nil {
			body = bytes.NewReader(init.Body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, body)
		if err != nil {
			return nil, &httpx.TypeError{Err: err}
		}
		for name, values := range init.Header {
			for _, value := range values {
				req.Header.Add(name, value)
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			if refused, ok := clierror.As(err); ok {
				return nil, refused
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, &httpx.TypeError{Err: err}
		}
		return resp, nil
	}
}
