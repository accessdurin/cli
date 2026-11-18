package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/accessdurin/cli/internal/clierror"
)

// roundTrip is an http.RoundTripper made of a function, for fake servers.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// recorded is one request a fake server received.
type recorded struct {
	Method, URL, Body string
	Header            http.Header
}

// fakeClient answers each request with reply and records it.
func fakeClient(reply func(r recorded) (int, string)) (*http.Client, func() []recorded) {
	var mu sync.Mutex
	var requests []recorded
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body := ""
		if r.Body != nil {
			data, _ := io.ReadAll(r.Body)
			body = string(data)
		}
		entry := recorded{Method: r.Method, URL: r.URL.String(), Body: body, Header: r.Header.Clone()}
		mu.Lock()
		requests = append(requests, entry)
		mu.Unlock()
		status, text := reply(entry)
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(text)), Request: r}, nil
	})}
	return client, func() []recorded {
		mu.Lock()
		defer mu.Unlock()
		return append([]recorded(nil), requests...)
	}
}

func TestAccountApiRefreshesOnceWithoutChangingTheMutation(t *testing.T) {
	for _, retry := range []int{200, 401} {
		token, refreshes := "old", 0
		client, requests := fakeClient(func(r recorded) (int, string) {
			if r.Header.Get("Authorization") == "Bearer old" || retry == 401 {
				return 401, `{"error":{"code":"unauthorized","message":"Expired"}}`
			}
			return 200, `{"ok":true}`
		})
		api := NewDurinAccountApi(AccountApiOptions{
			AppOrigin:   "https://app.example.test",
			AccessToken: func() string { return token },
			Fetch:       client,
			Refresh: func(context.Context) error {
				refreshes++
				token = "rotated"
				return nil
			},
		})
		err := api.Start(context.Background())
		if retry == 200 && err != nil {
			t.Fatal(err)
		}
		if retry == 401 {
			wantCliError(t, err, "Durin request failed: unauthorized (401).")
		}
		got := requests()
		if refreshes != 1 || len(got) != 2 {
			t.Fatalf("retry %d: refreshes %d, requests %d", retry, refreshes, len(got))
		}
		for i, bearer := range []string{"Bearer old", "Bearer rotated"} {
			r := got[i]
			if r.Header.Get("Authorization") != bearer || r.Body != "{}" || r.Method != "POST" || r.URL != "https://app.example.test/api/v1/onboarding/start" {
				t.Errorf("retry %d request %d: %+v", retry, i, r)
			}
			if r.Header.Get("Origin") != "https://app.example.test" || r.Header.Get("X-Requested-With") != "durin" || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("retry %d request %d headers: %v", retry, i, r.Header)
			}
		}
	}
}

func TestAccountApiErrors(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{403, `{"error":{"code":"forbidden","message":"No"}}`, "Durin request failed: forbidden (403)."},
		// Codes that could carry terminal controls or other text are never shown.
		{500, `{"error":{"code":"\u001b[2Jpwned","message":"x"}}`, "Durin request failed: invalid_response (500)."},
		{500, `{"error":{"code":"` + strings.Repeat("a", 101) + `","message":"x"}}`, "Durin request failed: invalid_response (500)."},
		{502, `{"unexpected":true}`, "Durin request failed: invalid_response (502)."},
		// /[a-z_]/iu folds the long s and the Kelvin sign, as JavaScript does.
		{409, `{"error":{"code":"ſtale_K","message":"x"}}`, "Durin request failed: ſtale_K (409)."},
	}
	for _, c := range cases {
		client, _ := fakeClient(func(recorded) (int, string) { return c.status, c.body })
		api := NewDurinAccountApi(AccountApiOptions{AppOrigin: "https://app.example.test", AccessToken: StaticToken("t"), Fetch: client})
		_, err := api.Session(context.Background())
		wantCliError(t, err, c.want)
		var apiErr *AccountApiError
		if e, ok := err.(*AccountApiError); ok {
			apiErr = e
		}
		if apiErr == nil || apiErr.Status != c.status {
			t.Errorf("%s: want an AccountApiError with status %d, got %#v", c.body, c.status, err)
		}
	}
	// A body that is not JSON fails as fetch's json() does, before the status is considered:
	// an unexpected error.
	client, _ := fakeClient(func(recorded) (int, string) { return 500, "<html>" })
	api := NewDurinAccountApi(AccountApiOptions{AppOrigin: "https://app.example.test", AccessToken: StaticToken("t"), Fetch: client})
	if _, err := api.Status(context.Background()); err == nil {
		t.Fatal("want an error")
	} else if _, ok := clierror.As(err); ok {
		t.Fatalf("want an unexpected error, got %v", err)
	}
	// A BOM is ignored and the body is validated by its schema.
	client, requests := fakeClient(func(recorded) (int, string) {
		return 200, "\xef\xbb\xbf" + `{"organizationId":"org_1","workosOrganizationId":"org_wos","status":"ready"}`
	})
	api = NewDurinAccountApi(AccountApiOptions{AppOrigin: "https://app.example.test", AccessToken: StaticToken("t"), Fetch: client})
	created, err := api.CreateOrganization(context.Background(), OrganizationCreate{Name: " Acme ", IdempotencyKey: "314b1a21-beda-519c-a3ec-b6dcb9f7cedf"})
	if err != nil || created.WorkosOrganizationID != "org_wos" {
		t.Fatalf("%+v %v", created, err)
	}
	if body := requests()[0].Body; body != `{"name":" Acme ","idempotencyKey":"314b1a21-beda-519c-a3ec-b6dcb9f7cedf"}` {
		t.Errorf("body %s", body)
	}
}
