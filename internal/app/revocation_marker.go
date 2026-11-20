package app

import (
	"os"
	"path/filepath"
	"time"

	"github.com/accessdurin/cli/internal/clierror"
	"github.com/accessdurin/cli/internal/contracts"
	"github.com/accessdurin/cli/internal/jsjson"
	"github.com/accessdurin/cli/internal/ports"
	"github.com/accessdurin/cli/internal/zod"
)

// SignOutUnfinished is shown while a logout's revocation is unconfirmed.
const SignOutUnfinished = "Signing out of this durin profile hasn’t finished: the authorization server could not confirm that access was revoked, so MCP calls and durin tools are blocked for this profile. Run durin logout again to finish, or durin logout --force to sign out on this computer without the server."

func profileFile(directory, profileID, suffix string) (string, error) {
	if _, err := zod.Parse(contracts.ProfileIDSchema, jsjson.String(profileID)); err != nil {
		return "", err
	}
	return filepath.Join(directory, "revocations", profileID+suffix), nil
}

// FileRevocationMarker is <directory>/revocations/<id>.pending. Only the marker's presence
// matters, so a damaged or unreadable marker still blocks the profile, and so does a symlink
// in its place, even one whose target is missing.
type FileRevocationMarker struct{ Path string }

var _ ports.RevocationMarker = (*FileRevocationMarker)(nil)

// NewFileRevocationMarker returns the marker for a profile.
func NewFileRevocationMarker(directory, profileID string) (*FileRevocationMarker, error) {
	path, err := profileFile(directory, profileID, ".pending")
	if err != nil {
		return nil, err
	}
	return &FileRevocationMarker{Path: path}, nil
}

// Pending reports whether anything is at the marker path, or it cannot be checked.
func (m *FileRevocationMarker) Pending() (bool, error) {
	_, err := os.Lstat(m.Path)
	return err == nil || !missing(err), nil
}

// Mark records an unfinished sign-out.
func (m *FileRevocationMarker) Mark() error {
	return AtomicWrite(m.Path, jsjson.Stringify(jsjson.Obj(jsjson.KV("markedAt", jsjson.String(isoString(time.Now())))))+"\n")
}

// Clear removes the marker.
func (m *FileRevocationMarker) Clear() error { return rmForce(m.Path) }

// AssertSignOutFinished fails while the profile's sign-out is unfinished.
func AssertSignOutFinished(marker ports.RevocationMarker) error {
	pending, err := marker.Pending()
	if err != nil {
		return err
	}
	if pending {
		return clierror.New(SignOutUnfinished)
	}
	return nil
}

var pendingIndexSchema = zod.Object(
	zod.F("grants", zod.Array(zod.Object(
		zod.F("id", zod.UUID()),
		zod.F("clientId", zod.String().Min(1)),
		zod.F("addedAt", zod.ISODateTime()),
	).Strict())),
).Strict()

var pendingTokensSchema = zod.Object(
	zod.F("accessToken", zod.String().Min(1)),
	zod.F("refreshToken", zod.Optional(zod.String().Min(1))),
).Strict()

type indexEntry struct{ ID, ClientID, AddedAt string }

func (e indexEntry) json() *jsjson.Object {
	return jsjson.Obj(jsjson.KV("id", jsjson.String(e.ID)), jsjson.KV("clientId", jsjson.String(e.ClientID)), jsjson.KV("addedAt", jsjson.String(e.AddedAt)))
}

type storedGrant struct {
	entry indexEntry
	grant *contracts.UnrevokedGrant
}

func sameGrant(a contracts.UnrevokedGrant, b *contracts.UnrevokedGrant) bool {
	return b != nil && a.ClientID == b.ClientID && a.AccessToken == b.AccessToken && a.RefreshToken == b.RefreshToken
}

func tokensOf(grant contracts.UnrevokedGrant) string {
	o := jsjson.Obj(jsjson.KV("accessToken", jsjson.String(grant.AccessToken)))
	if grant.RefreshToken != "" {
		o.Set("refreshToken", jsjson.String(grant.RefreshToken))
	}
	return jsjson.Stringify(o)
}

// FilePendingRevocations keeps grants a new sign-in replaced before their revocation was
// confirmed. The tokens live in the OS credential store, one entry per grant, like the MCP
// credentials; the private index on disk holds only ids and timestamps. Callers hold the
// profile lock. An index that cannot be read is kept as it is rather than overwritten, so no
// grant is silently forgotten, and a credential store that cannot be used fails like the MCP
// credentials do.
type FilePendingRevocations struct {
	Path      string
	profileID string
	store     ports.CredentialStore
}

var _ ports.PendingRevocations = (*FilePendingRevocations)(nil)

// NewFilePendingRevocations returns the pending list for a profile.
func NewFilePendingRevocations(directory, profileID string, store ports.CredentialStore) (*FilePendingRevocations, error) {
	path, err := profileFile(directory, profileID, ".unrevoked.json")
	if err != nil {
		return nil, err
	}
	return &FilePendingRevocations{Path: path, profileID: profileID, store: store}, nil
}

// Entries lists the grants whose tokens are still readable.
func (p *FilePendingRevocations) Entries() ([]contracts.UnrevokedGrant, error) {
	stored, err := p.stored()
	if err != nil {
		return nil, err
	}
	grants := []contracts.UnrevokedGrant{}
	for _, s := range stored {
		if s.grant != nil {
			grants = append(grants, *s.grant)
		}
	}
	return grants, nil
}

// Add records a grant. The index entry comes first, so a crash never leaves tokens in the
// store untracked.
func (p *FilePendingRevocations) Add(grant contracts.UnrevokedGrant) error {
	stored, err := p.stored()
	if err != nil {
		return err
	}
	index := make([]indexEntry, 0, len(stored))
	for _, s := range stored {
		if sameGrant(grant, s.grant) {
			return nil
		}
		index = append(index, s.entry)
	}
	entry := indexEntry{ID: randomUUID(), ClientID: grant.ClientID, AddedAt: isoString(time.Now())}
	if err := p.writeIndex(append(append([]indexEntry(nil), index...), entry)); err != nil {
		return err
	}
	if err := p.store.Write(p.key(entry.ID), tokensOf(grant)); err != nil {
		_ = p.writeIndex(index)
		return err
	}
	return nil
}

// Replace keeps only grants. Tokens leave the store before their index entry, for the same
// reason as in Add.
func (p *FilePendingRevocations) Replace(grants []contracts.UnrevokedGrant) error {
	stored, err := p.stored()
	if err != nil {
		return err
	}
	var kept []indexEntry
	var firstErr error
	for _, s := range stored {
		keep := false
		for _, g := range grants {
			if sameGrant(g, s.grant) {
				keep = true
			}
		}
		if keep {
			kept = append(kept, s.entry)
		} else if err := p.forget(s); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return p.writeIndex(kept)
}

func (p *FilePendingRevocations) stored() ([]storedGrant, error) {
	index, err := p.readIndex()
	if err != nil {
		return nil, err
	}
	stored := make([]storedGrant, len(index))
	for i, entry := range index {
		grant, err := p.readGrant(entry)
		if err != nil {
			return nil, err
		}
		stored[i] = storedGrant{entry: entry, grant: grant}
	}
	return stored, nil
}

func (p *FilePendingRevocations) readGrant(entry indexEntry) (*contracts.UnrevokedGrant, error) {
	raw, found, err := p.store.Read(p.key(entry.ID))
	if err != nil {
		return nil, err
	}
	var value jsjson.Value = jsjson.Null{}
	if found {
		if parsed, err := jsjson.ParseString(raw); err == nil {
			value = parsed
		}
	}
	out, zerr := zod.SafeParse(pendingTokensSchema, value)
	if zerr != nil {
		return nil, nil
	}
	o := out.(*jsjson.Object)
	grant := contracts.UnrevokedGrant{ClientID: entry.ClientID}
	grant.AccessToken, _ = o.GetString("accessToken")
	grant.RefreshToken, _ = o.GetString("refreshToken")
	return &grant, nil
}

// forget removes a grant's tokens. An entry without readable tokens can never be revoked, so
// removing it is best effort.
func (p *FilePendingRevocations) forget(s storedGrant) error {
	err := p.store.Remove(p.key(s.entry.ID))
	if s.grant == nil {
		return nil
	}
	return err
}

func (p *FilePendingRevocations) readIndex() ([]indexEntry, error) {
	raw, found, err := p.read()
	if err != nil || !found {
		return nil, err
	}
	value, perr := jsjson.ParseString(raw)
	if perr != nil {
		return nil, p.unreadable()
	}
	out, zerr := zod.SafeParse(pendingIndexSchema, value)
	if zerr != nil {
		return nil, p.unreadable()
	}
	grants, _ := out.(*jsjson.Object).GetArray("grants")
	entries := make([]indexEntry, len(grants))
	for i, g := range grants {
		o := g.(*jsjson.Object)
		entries[i].ID, _ = o.GetString("id")
		entries[i].ClientID, _ = o.GetString("clientId")
		entries[i].AddedAt, _ = o.GetString("addedAt")
	}
	return entries, nil
}

func (p *FilePendingRevocations) writeIndex(grants []indexEntry) error {
	if len(grants) == 0 {
		return rmForce(p.Path)
	}
	list := jsjson.Array{}
	for _, g := range grants {
		list = append(list, g.json())
	}
	return AtomicWrite(p.Path, jsjson.Stringify(jsjson.Obj(jsjson.KV("grants", list)))+"\n")
}

// read returns the index text. Anything but a regular file here, a dangling symlink included,
// is unreadable, not absent.
func (p *FilePendingRevocations) read() (string, bool, error) {
	info, err := os.Lstat(p.Path)
	if err != nil {
		if missing(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, p.unreadable()
	}
	raw, err := readText(p.Path)
	return raw, err == nil, err
}

func (p *FilePendingRevocations) unreadable() error {
	return clierror.New("durin can’t read its list of earlier sign-ins that still need to be revoked (" + p.Path + "). Move that file aside, then run durin again.")
}

func (p *FilePendingRevocations) key(id string) string {
	return "mcp:" + p.profileID + ":unrevoked:" + id
}
