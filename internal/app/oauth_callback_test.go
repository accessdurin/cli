package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const callbackIssuer = "https://auth.example.test/"

func callbackURL(redirect string, params ...[2]string) string {
	values := url.Values{}
	for _, p := range params {
		values.Add(p[0], p[1])
	}
	return redirect + "?" + values.Encode()
}

func httpGet(t *testing.T, target string) (int, http.Header, string) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
	resp, err := client.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(body)
}

func TestLoopbackAuthorizationAcceptsOneValidatedCode(t *testing.T) {
	callback, err := ListenForAuthorization(callbackIssuer, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer callback.Close()
	if !strings.HasPrefix(callback.RedirectURL, "http://127.0.0.1:") || !strings.HasSuffix(callback.RedirectURL, "/oauth/callback") || len(callback.State) != 43 {
		t.Fatalf("redirect %s state %s", callback.RedirectURL, callback.State)
	}
	wrong := callbackURL(callback.RedirectURL, [2]string{"code", "synthetic-code"}, [2]string{"state", "wrong"}, [2]string{"iss", callbackIssuer})
	if status, _, body := httpGet(t, wrong); status != 400 || body != "Invalid authorization callback." {
		t.Fatalf("wrong state: %d %q", status, body)
	}
	valid := callbackURL(callback.RedirectURL, [2]string{"code", "synthetic-code"}, [2]string{"state", callback.State}, [2]string{"iss", callbackIssuer})
	status, header, body := httpGet(t, valid)
	if status != 200 || body != "Durin authorization received. Return to your terminal to finish setup." {
		t.Fatalf("valid: %d %q", status, body)
	}
	for name, want := range map[string]string{"Content-Type": "text/plain; charset=utf-8", "Cache-Control": "no-store", "Referrer-Policy": "no-referrer"} {
		if header.Get(name) != want {
			t.Errorf("%s: %q", name, header.Get(name))
		}
	}
	code, err := callback.Result(context.Background())
	if err != nil || code != (AuthorizationCode{Code: "synthetic-code", Issuer: callbackIssuer}) {
		t.Fatalf("%+v %v", code, err)
	}
	// The listener is closed once a code is received.
	client := &http.Client{Timeout: time.Second}
	if resp, err := client.Get(valid); err == nil {
		resp.Body.Close()
		t.Fatal("the callback is still listening")
	}
}

func TestLoopbackAuthorizationDeniedAndExpired(t *testing.T) {
	callback, err := ListenForAuthorization(callbackIssuer, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer callback.Close()
	// No iss is accepted (the provider may omit it).
	denied := callbackURL(callback.RedirectURL, [2]string{"error", "access_denied"}, [2]string{"state", callback.State})
	if status, _, body := httpGet(t, denied); status != 200 || body != "Durin authorization was denied. Return to your terminal." {
		t.Fatalf("%d %q", status, body)
	}
	_, err = callback.Result(context.Background())
	wantCliError(t, err, "MCP authorization was denied.")

	expiring, err := ListenForAuthorization(callbackIssuer, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_, err = expiring.Result(context.Background())
	wantCliError(t, err, "MCP authorization expired. Run durin to sign in again.")

	closed, err := ListenForAuthorization(callbackIssuer, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	_, err = closed.Result(context.Background())
	wantCliError(t, err, "MCP authorization interrupted.")
}

func TestCallbackInputIsBoundToTheTransaction(t *testing.T) {
	binding := callbackBinding{RedirectURL: "http://127.0.0.1:4567/oauth/callback", State: "state-1", Issuer: callbackIssuer}
	request := func(method, target, host string) *http.Request {
		r := httptest.NewRequest(method, target, nil)
		r.Host = host
		r.RequestURI = target
		return r
	}
	const host = "127.0.0.1:4567"
	good := "/oauth/callback?code=c&state=state-1&iss=" + url.QueryEscape(callbackIssuer)
	if input := callbackInput(request("GET", good, host), binding); input == nil || input.Kind != "code" || input.Code != "c" || input.Issuer != callbackIssuer {
		t.Fatalf("%+v", input)
	}
	// An absolute-form target on the same origin is the same callback.
	if callbackInput(request("GET", "http://127.0.0.1:4567"+good, host), binding) == nil {
		t.Error("refused an absolute-form target")
	}
	for name, r := range map[string]*http.Request{
		"POST":            request("POST", good, host),
		"another Host":    request("GET", good, "localhost:4567"),
		"another path":    request("GET", "/oauth/callback/?code=c&state=state-1", host),
		"another origin":  request("GET", "http://127.0.0.2:4567"+good, host),
		"another issuer":  request("GET", "/oauth/callback?code=c&state=state-1&iss=https%3A%2F%2Fevil.test%2F", host),
		"a longer state":  request("GET", "/oauth/callback?code=c&state=state-10", host),
		"two states":      request("GET", "/oauth/callback?code=c&state=state-1&state=state-1", host),
		"two codes":       request("GET", "/oauth/callback?code=c&code=d&state=state-1", host),
		"code and error":  request("GET", "/oauth/callback?code=c&error=access_denied&state=state-1", host),
		"neither":         request("GET", "/oauth/callback?state=state-1", host),
		"an empty code":   request("GET", "/oauth/callback?code=&state=state-1", host),
		"a state too big": request("GET", "/oauth/callback?code=c&state="+strings.Repeat("s", 257), host),
	} {
		if input := callbackInput(r, binding); input != nil {
			t.Errorf("%s: accepted %+v", name, input)
		}
	}
}
