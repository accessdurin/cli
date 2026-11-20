package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/mcp/httpx"
	"github.com/accessdurin/cli/internal/mcp/oauth"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/weburl"
	"github.com/accessdurin/cli/internal/zod"
)

// Pinned production hosts for logging out a refused profile (src/refused-logout.ts). They are
// never taken from the saved profile.
const (
	productionApp      = "https://app.getdurin.com"
	productionResource = "https://mcp.getdurin.com"
	productionIssuer   = "https://auth.getdurin.com"
	productionHost     = "auth.getdurin.com"
	untrustedSession   = "The server session was not revoked because its issuer isn't trusted; it will expire on its own."
	notRevoked         = "The server session was not revoked; it will expire on its own."
)

// refusedLogout is what logging out a refused profile needs. Base is the HTTP client under the
// production secure fetch (nil: http.DefaultClient).
type refusedLogout struct {
	Directory string
	ID        string
	Reason    string
	Store     ports.CredentialStore
	Base      *http.Client
}

func stringField(o map[string]any, key string) string {
	s, _ := o[key].(string)
	return s
}

func jsonObject(text string) map[string]any {
	var o map[string]any
	if json.Unmarshal([]byte(text), &o) != nil {
		return map[string]any{}
	}
	return o
}

// productionIssuerSpelling is exactly the production issuer, or that issuer plus one slash.
func productionIssuerSpelling(issuer string) bool {
	return strings.TrimSuffix(issuer, "/") == productionIssuer
}

func resourceOrigin(resource string) string {
	u, err := weburl.Parse(resource)
	if err != nil {
		return ""
	}
	return u.Origin()
}

// trustedProduction: only a profile naming production's app, resource and issuer may have its
// grant revoked, and then only at production's pinned host.
func trustedProduction(raw map[string]any) bool {
	return stringField(raw, "appOrigin") == productionApp &&
		resourceOrigin(stringField(raw, "resource")) == productionResource &&
		productionIssuerSpelling(stringField(raw, "mcpIssuer"))
}

// selectedProfileID is --profile when given, else the active profile.
func selectedProfileID(options *CliOptions) (string, error) {
	if id, given := options.Values.String("profile"); given {
		return id, nil
	}
	raw, err := readText(filepath.Join(options.Directory, "active"))
	if err != nil {
		return "", err
	}
	return strings.TrimFunc(raw, zod.IsJSWhiteSpace), nil
}

type heldToken struct{ token, hint string }

func storedToken(store ports.CredentialStore, id string) heldToken {
	text, found, err := store.Read("mcp:" + id + ":tokens")
	if err != nil || !found {
		return heldToken{hint: "access_token"}
	}
	parsed := jsonObject(text)
	if refresh := stringField(parsed, "refresh_token"); refresh != "" {
		return heldToken{refresh, "refresh_token"}
	}
	return heldToken{stringField(parsed, "access_token"), "access_token"}
}

func storedClientID(store ports.CredentialStore, id string) string {
	text, found, err := store.Read("mcp:" + id + ":client")
	if err != nil || !found {
		return ""
	}
	return stringField(jsonObject(text), "client_id")
}

// authorityOf is the authority as written, so an explicit port or userinfo is not folded away.
func authorityOf(endpoint string) string {
	scheme := strings.Index(endpoint, "://")
	if scheme < 0 {
		return ""
	}
	rest := endpoint[scheme+3:]
	if end := strings.IndexAny(rest, "/?#"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// pinnedHTTPSHost is HTTPS on the pinned host, with no userinfo and no explicit port.
func pinnedHTTPSHost(endpoint string) bool {
	u, err := weburl.Parse(endpoint)
	return err == nil && u.Protocol() == "https:" && authorityOf(endpoint) == productionHost
}

func (l refusedLogout) productionFetch() httpx.Fetch {
	return HTTPFetch(SecureFetch(contracts.CliHostAllowlist{Hosts: []string{productionHost}}, l.Base))
}

// advertisedRevocation is only an endpoint production's own metadata names, on the pinned host.
func (l refusedLogout) advertisedRevocation(ctx context.Context) string {
	metadata, err := oauth.DiscoverAuthorizationServerMetadata(ctx, productionIssuer, oauth.AuthorizationServerDiscoveryOptions{Fetch: l.productionFetch()})
	if err != nil || metadata == nil || metadata.RevocationEndpoint == nil || !pinnedHTTPSHost(*metadata.RevocationEndpoint) {
		return ""
	}
	u, _ := weburl.Parse(*metadata.RevocationEndpoint)
	return u.Href()
}

func (l refusedLogout) postRevoke(ctx context.Context, endpoint string, held heldToken, clientID string) bool {
	header := http.Header{}
	header.Set("content-type", "application/x-www-form-urlencoded")
	body := weburl.NewSearchParams([2]string{"token", held.token}, [2]string{"token_type_hint", held.hint}, [2]string{"client_id", clientID}).String()
	resp, err := l.productionFetch()(ctx, endpoint, &httpx.RequestInit{Method: http.MethodPost, Header: header, Body: []byte(body)})
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return httpx.OK(resp)
}

func (l refusedLogout) revokeProduction(ctx context.Context) bool {
	held := storedToken(l.Store, l.ID)
	clientID := storedClientID(l.Store, l.ID)
	if held.token == "" || clientID == "" {
		return false
	}
	endpoint := l.advertisedRevocation(ctx)
	if endpoint == "" {
		return false
	}
	return l.postRevoke(ctx, endpoint, held, clientID)
}

func (l refusedLogout) clearUnrevokedKeys(path string) {
	var index struct {
		Grants []struct {
			ID string `json:"id"`
		} `json:"grants"`
	}
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &index) == nil {
		for _, grant := range index.Grants {
			if grant.ID != "" {
				_ = l.Store.Remove("mcp:" + l.ID + ":unrevoked:" + grant.ID)
			}
		}
	}
}

func (l refusedLogout) clearRevocations() error {
	revocations := filepath.Join(l.Directory, "revocations")
	entries, err := os.ReadDir(revocations)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), l.ID) {
			continue
		}
		path := filepath.Join(revocations, entry.Name())
		if entry.Name() == l.ID+".unrevoked.json" {
			l.clearUnrevokedKeys(path)
		}
		if err := rmForce(path); err != nil {
			return err
		}
	}
	return nil
}

func (l refusedLogout) clearActive() error {
	active := filepath.Join(l.Directory, "active")
	current, err := readText(active)
	if err == nil && strings.TrimFunc(current, zod.IsJSWhiteSpace) == l.ID {
		return rmForce(active)
	}
	return nil
}

func (l refusedLogout) clear() error {
	_ = l.Store.Remove("mcp:" + l.ID + ":tokens")
	_ = l.Store.Remove("mcp:" + l.ID + ":client")
	if err := l.clearRevocations(); err != nil {
		return err
	}
	if err := rmForce(filepath.Join(l.Directory, "profiles", l.ID+".json")); err != nil {
		return err
	}
	return l.clearActive()
}

func refusedDetail(id string, revoked *bool) string {
	removed := "Removed local credentials for " + id + "."
	switch {
	case revoked == nil:
		return removed + " " + untrustedSession
	case *revoked:
		return removed + " The server session was revoked."
	default:
		return removed + " " + notRevoked
	}
}

// finish runs under the profile lock. A profile another logout already removed is reported as
// logged out.
func (l refusedLogout) finish(ctx context.Context, io *IO) error {
	path := filepath.Join(l.Directory, "profiles", l.ID+".json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		io.ExitCode = 0
		_, err := io.Stdout.Write([]byte("already logged out\n"))
		return err
	}
	raw := jsonObject(string(data))
	var revoked *bool
	if trustedProduction(raw) {
		done := l.revokeProduction(ctx)
		revoked = &done
	}
	if err := l.clear(); err != nil {
		return err
	}
	return PrintJSON(io.Stdout, jsjson.Obj(
		jsjson.KV("profile", jsjson.String(l.ID)),
		jsjson.KV("removed", jsjson.Bool(true)),
		jsjson.KV("reason", jsjson.String(l.Reason)),
		jsjson.KV("detail", jsjson.String(refusedDetail(l.ID, revoked))),
	))
}

// logoutRefusedProfile deletes a profile that can no longer be loaded, with its credentials
// and pending revocations. It revokes the server grant only for a real production profile, at
// production's pinned host.
func logoutRefusedProfile(ctx context.Context, options *CliOptions, io *IO, reason string, store ports.CredentialStore, base *http.Client) error {
	id, err := selectedProfileID(options)
	if err != nil {
		return err
	}
	if !contracts.ValidProfileID(id) {
		return errors.New("invalid profile id")
	}
	logout := refusedLogout{Directory: options.Directory, ID: id, Reason: reason, Store: store, Base: base}
	_, err = WithProfileLock(options.Directory, id, func() (struct{}, error) {
		return struct{}{}, logout.finish(ctx, io)
	}, DefaultLockPatience)
	return err
}
