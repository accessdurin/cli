package e2e

import (
	"fmt"
	"path/filepath"
	"testing"
)

func (e *Env) revocationState(id string) {
	e.Note("revocations directory", e.Tree(filepath.Join(e.Config, "revocations")))
	e.Note("credentials", e.CredentialKeys("mcp:"+id))
}

func TestLogout(t *testing.T) {
	e, id := signedIn(t)
	e.Run("", "logout", "--config-dir", e.Config)
	e.NoteRequests()
	e.revocationState(id)
	e.Run("", "verify", "--config-dir", e.Config)

	// Nothing stored: there is nothing to revoke.
	e.Run("", "logout", "--config-dir", e.Config)
	e.NoteRequests()

	// A server without a revocation endpoint.
	e.Server.Revocation = false
	e.SeedGrant(id, "access-seeded", "refresh-seeded")
	e.Run("", "logout", "--profile", id, "--config-dir", e.Config)
	e.NoteRequests()
	e.revocationState(id)
}

func TestLogoutUnconfirmed(t *testing.T) {
	e, id := signedIn(t)
	e.Server.RevocationStatus = 503
	e.Run("", "logout", "--config-dir", e.Config)
	e.NoteRequests()
	e.revocationState(id)
	// Until sign-out finishes, the profile is blocked everywhere.
	e.Run("", "tools", "list", "--config-dir", e.Config)
	e.Run("", "verify", "--config-dir", e.Config)
	e.NoteRequests()
	// A 4xx is not confirmation either.
	e.Server.RevocationStatus = 400
	e.Run("", "logout", "--config-dir", e.Config)
	e.NoteRequests()
	// --force clears local state even though the server never confirmed.
	e.Run("", "logout", "--force", "--config-dir", e.Config)
	e.NoteRequests()
	e.revocationState(id)
	// Once the server works again, a new logout has nothing left.
	e.Server.RevocationStatus = 200
	e.Run("", "logout", "--config-dir", e.Config)
	e.NoteRequests()
}

func TestPendingRevocations(t *testing.T) {
	e, id := signedIn(t)
	e.Server.Pages = [][]Tool{}
	grant := "6f1c2b9e-0d4a-4c3b-9a7e-2f5d8c1b0a99"
	index := fmt.Sprintf(`{"grants":[{"id":%q,"clientId":"client-old","addedAt":"2026-01-01T00:00:00.000Z"}]}`+"\n", grant)
	e.WriteFile(filepath.Join(e.Config, "revocations", id+".unrevoked.json"), index, 0o600)
	e.StoreCredential("mcp:"+id+":unrevoked:"+grant, `{"accessToken":"old-access","refreshToken":"old-refresh"}`)

	// A failed retry keeps the grant for later.
	e.Server.RevocationStatus = 500
	e.Run("", "verify", "--config-dir", e.Config)
	e.NoteRequests()
	e.revocationState(id)

	// A successful retry forgets it.
	e.Server.RevocationStatus = 200
	e.Run("", "verify", "--config-dir", e.Config)
	e.NoteRequests()
	e.revocationState(id)

	// An unreadable index is kept, never overwritten, and reported by commands that need it.
	e.WriteFile(filepath.Join(e.Config, "revocations", id+".unrevoked.json"), "{broken", 0o600)
	e.Run("", "verify", "--config-dir", e.Config)
	e.Run("", "logout", "--config-dir", e.Config)
	e.NoteRequests()
	e.revocationState(id)
}

func TestProfileLock(t *testing.T) {
	e, id := signedIn(t)
	// A lock left by a process that no longer exists is reported, never silently broken.
	e.WriteFile(filepath.Join(e.Config, "locks", id+".lock"), `{"pid":999999,"nonce":"6f1c2b9e-0d4a-4c3b-9a7e-2f5d8c1b0a99"}`, 0o600)
	e.Run("", "verify", "--config-dir", e.Config)
	e.Run("", "logout", "--config-dir", e.Config)
	e.NoteRequests()
	e.Note("locks", e.Tree(filepath.Join(e.Config, "locks")))
}
