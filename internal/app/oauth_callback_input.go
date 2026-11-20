package app

import (
	"crypto/subtle"
	"net/http"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/weburl"
	"github.com/accessdurin/cli/internal/zod"
)

// CallbackInput is authorizationCallbackSchema's output: Kind is code (with Code) or denied.
// Issuer is empty when the callback carried no iss.
type CallbackInput struct {
	Kind   string
	Code   string
	State  string
	Issuer string
}

// callbackBinding is the authorization transaction a callback must belong to.
type callbackBinding struct {
	RedirectURL string
	State       string
	Issuer      string
}

// sameState compares states in constant time.
func sameState(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func parseBoundInput(url *weburl.URL, binding callbackBinding) *CallbackInput {
	fields := jsjson.NewObject()
	for _, kv := range url.SearchParams().Entries() {
		fields.Set(kv[0], jsjson.String(kv[1]))
	}
	parsed, issues := zod.SafeParse(contracts.AuthorizationCallbackSchema, fields)
	if issues != nil {
		return nil
	}
	o := parsed.(*jsjson.Object)
	input := &CallbackInput{}
	input.Kind, _ = o.GetString("kind")
	input.Code, _ = o.GetString("code")
	input.State, _ = o.GetString("state")
	issuer, hasIssuer := o.GetString("issuer")
	input.Issuer = issuer
	if !sameState(input.State, binding.State) || (hasIssuer && issuer != binding.Issuer) {
		return nil
	}
	return input
}

// callbackInput is the OAuth callback a request carries, or nil unless it is a GET to the
// exact redirect URL, on its host, with at most one of each OAuth parameter, for this
// transaction's state and (when it names one) issuer (src/oauth-callback-input.ts).
func callbackInput(request *http.Request, binding callbackBinding) *CallbackInput {
	target := request.RequestURI
	if target == "" {
		target = "/"
	}
	url, err := weburl.ParseRef(target, binding.RedirectURL)
	if err != nil {
		return nil
	}
	expected, err := weburl.Parse(binding.RedirectURL)
	if err != nil {
		return nil
	}
	params := url.SearchParams()
	single := true
	for _, key := range []string{"code", "state", "iss", "error"} {
		single = single && len(params.GetAll(key)) <= 1
	}
	valid := request.Method == http.MethodGet && request.Host == expected.Host() &&
		url.Origin() == expected.Origin() && url.Pathname() == expected.Pathname() && single
	if !valid {
		return nil
	}
	return parseBoundInput(url, binding)
}
