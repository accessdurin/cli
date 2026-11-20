// Package httpx is the fetch abstraction of the MCP port. Every HTTP request the port makes goes
// through an injected Fetch function (the CLI wraps it with a host allowlist and a no-redirect
// policy); nothing here uses a default HTTP client.
//
// It mirrors the SDK helpers createFetchWithInit, fetchWithinOrigin and unfollowedRedirect
// (core-internal/src/shared/transport.ts).
package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/weburl"
)

// RedirectMode is RequestInit.redirect.
type RedirectMode string

// Redirect modes. The empty mode means "not set" (fetch's default is follow).
const (
	RedirectFollow RedirectMode = "follow"
	RedirectError  RedirectMode = "error"
	RedirectManual RedirectMode = "manual"
)

// RequestInit is the subset of the fetch RequestInit the port uses. A nil Body sends no body.
type RequestInit struct {
	Method   string
	Header   http.Header
	Body     []byte
	Redirect RedirectMode
}

// Clone copies the init (headers deeply).
func (r *RequestInit) Clone() *RequestInit {
	if r == nil {
		return &RequestInit{}
	}
	c := *r
	if r.Header != nil {
		c.Header = r.Header.Clone()
	}
	return &c
}

// Fetch performs one HTTP exchange, like the global fetch. The context plays the role of
// RequestInit.signal. Implementations follow, refuse or return redirects according to
// init.Redirect. The returned response body must be closed by the caller.
type Fetch func(ctx context.Context, url string, init *RequestInit) (*http.Response, error)

// TypeError marks failures that JavaScript's fetch reports as TypeError (network failures,
// refused redirects under redirect: 'error', invalid URLs). The SDK treats TypeError specially in
// a few places (protected-resource discovery rethrows it instead of falling back).
type TypeError struct{ Err error }

func (e *TypeError) Error() string { return "fetch failed: " + e.Err.Error() }
func (e *TypeError) Unwrap() error { return e.Err }

// IsTypeError reports whether err is a JavaScript TypeError equivalent.
func IsTypeError(err error) bool {
	var te *TypeError
	return errors.As(err, &te) || errors.Is(err, weburl.ErrInvalidURL)
}

// NewHTTPFetch adapts an explicit *http.Client to Fetch, honoring redirect modes. Transport
// errors are reported as *TypeError, as Node's fetch does.
func NewHTTPFetch(client *http.Client) Fetch {
	return func(ctx context.Context, url string, init *RequestInit) (*http.Response, error) {
		if init == nil {
			init = &RequestInit{}
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
			return nil, &TypeError{err}
		}
		for k, vs := range init.Header {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		c := *client
		switch init.Redirect {
		case RedirectError:
			c.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("unexpected redirect") }
		case RedirectManual:
			c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		}
		resp, err := c.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, &TypeError{err}
		}
		return resp, nil
	}
}

// OK reports whether the status is 2xx (Response.ok).
func OK(resp *http.Response) bool { return resp.StatusCode >= 200 && resp.StatusCode <= 299 }

// StatusText returns the reason phrase (Response.statusText).
func StatusText(resp *http.Response) string {
	code := strconv.Itoa(resp.StatusCode)
	if strings.HasPrefix(resp.Status, code) {
		return strings.TrimSpace(strings.TrimPrefix(resp.Status, code))
	}
	return resp.Status
}

// ResponseURL returns Response.url, falling back to the requested URL.
func ResponseURL(resp *http.Response, requested string) string {
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String()
	}
	return requested
}

// Text reads and closes the body, decoding UTF-8 like Response#text() (BOM stripped, invalid
// sequences replaced).
func Text(resp *http.Response) (string, error) {
	if resp.Body == nil {
		return "", nil
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return jsjson.DecodeUTF8StripBOM(b), nil
}

// Discard reads and closes the body, ignoring errors (`await response.text?.().catch(() => {})`).
func Discard(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// JSON reads the body like Response#json(): Text then JSON.parse.
func JSON(resp *http.Response) (jsjson.Value, error) {
	text, err := Text(resp)
	if err != nil {
		return nil, err
	}
	return jsjson.ParseString(text)
}

func mergeHeaders(base, over http.Header) http.Header {
	out := http.Header{}
	for k, v := range base {
		out[k] = append([]string(nil), v...)
	}
	for k, v := range over {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// WithInit is createFetchWithInit: every call inherits baseInit, with per-call fields taking
// precedence and headers merged when the call supplies any.
func WithInit(baseFetch Fetch, baseInit *RequestInit) Fetch {
	if baseInit == nil {
		return baseFetch
	}
	return func(ctx context.Context, url string, init *RequestInit) (*http.Response, error) {
		merged := baseInit.Clone()
		if init != nil {
			if init.Method != "" {
				merged.Method = init.Method
			}
			if init.Body != nil {
				merged.Body = init.Body
			}
			if init.Redirect != "" {
				merged.Redirect = init.Redirect
			}
			if init.Header != nil {
				merged.Header = mergeHeaders(baseInit.Header, init.Header)
			}
		}
		return baseFetch(ctx, url, merged)
	}
}

var redirectStatuses = map[int]bool{301: true, 302: true, 303: true, 307: true, 308: true}

const maxRedirects = 5

func isWithinOrigin(from, to *weburl.URL) bool {
	if from.Protocol() == to.Protocol() && from.Host() == to.Host() {
		return true
	}
	return from.Protocol() == "http:" && to.Protocol() == "https:" && from.Hostname() == to.Hostname() && from.Port() == "" && to.Port() == ""
}

func redirectTarget(url string, resp *http.Response) *weburl.URL {
	if !redirectStatuses[resp.StatusCode] {
		return nil
	}
	location := resp.Header.Get("location")
	if location == "" {
		return nil
	}
	target, err := weburl.ParseRef(location, url)
	if err != nil {
		return nil
	}
	return target
}

// WithinOrigin is fetchWithinOrigin: unless the call asks for redirect 'error' or 'manual', it
// fetches with redirect 'manual' and follows a redirect only when it keeps the method (GET, 307,
// 308), keeps userinfo and stays within the request's origin, at most five times.
func WithinOrigin(baseFetch Fetch) Fetch {
	return func(ctx context.Context, url string, init *RequestInit) (*http.Response, error) {
		if init != nil && (init.Redirect == RedirectError || init.Redirect == RedirectManual) {
			return baseFetch(ctx, url, init)
		}
		method := "GET"
		if init != nil && init.Method != "" {
			method = strings.ToUpper(init.Method)
		}
		current := url
		for followed := 0; ; followed++ {
			call := init.Clone()
			call.Redirect = RedirectManual
			resp, err := baseFetch(ctx, current, call)
			if err != nil {
				return nil, err
			}
			target := redirectTarget(current, resp)
			if target == nil || followed == maxRedirects {
				return resp, nil
			}
			from, err := weburl.Parse(current)
			if err != nil {
				return nil, &TypeError{err}
			}
			keepsMethod := method == "GET" || resp.StatusCode == 307 || resp.StatusCode == 308
			keepsUserinfo := (target.Username() == "" && target.Password() == "") || (target.Username() == from.Username() && target.Password() == from.Password())
			if !keepsMethod || !keepsUserinfo || !isWithinOrigin(from, target) {
				return resp, nil
			}
			Discard(resp)
			current = target.Href()
		}
	}
}

// UnfollowedRedirect returns the SDK's error text for a redirect response that was not followed,
// or "" for any other response.
func UnfollowedRedirect(url string, resp *http.Response) string {
	respURL := ResponseURL(resp, url)
	target := redirectTarget(respURL, resp)
	if target == nil {
		return ""
	}
	target.SetUsername("")
	target.SetPassword("")
	target.SetSearch("")
	target.SetHash("")
	if from, err := weburl.Parse(respURL); err == nil && target.Protocol() == "http:" && from.Protocol() == "https:" {
		target.SetProtocol("https:")
		return fmt.Sprintf("Redirect from https to plain http not followed; try %s as the endpoint", target.Href())
	}
	return fmt.Sprintf("Redirect to %s not followed; use that URL as the endpoint if it is the intended server", target.Href())
}
