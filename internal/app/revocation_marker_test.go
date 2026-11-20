package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/accessdurin/cli/internal/contracts"
)

var testProfileID = strings.Repeat("a", 64)

func TestRevocationMarkerBlocksUntilCleared(t *testing.T) {
	dir := tempDir(t)
	first, _ := NewFileRevocationMarker(dir, testProfileID)
	second, _ := NewFileRevocationMarker(dir, testProfileID)
	var wg sync.WaitGroup
	for _, m := range []*FileRevocationMarker{first, second, first} {
		wg.Add(1)
		go func() { defer wg.Done(); _ = m.Mark() }()
	}
	wg.Wait()
	for _, m := range []*FileRevocationMarker{first, second} {
		if pending, _ := m.Pending(); !pending {
			t.Fatal("marked profile not pending")
		}
	}
	wantCliError(t, AssertSignOutFinished(first), "durin logout again")
	_ = second.Clear()
	_ = second.Clear()
	if pending, _ := first.Pending(); pending {
		t.Fatal("cleared profile still pending")
	}
	// Only presence matters: a damaged marker or a dangling symlink still blocks.
	writeFile(t, first.Path, "\x00not a marker")
	if pending, _ := first.Pending(); !pending {
		t.Fatal("damaged marker not pending")
	}
	if runtime.GOOS != "windows" {
		_ = os.Remove(first.Path)
		_ = os.Symlink(filepath.Join(dir, "nowhere"), first.Path)
		if pending, _ := first.Pending(); !pending {
			t.Fatal("dangling symlink not pending")
		}
	}
	other, _ := NewFileRevocationMarker(dir, strings.Repeat("b", 64))
	if pending, _ := other.Pending(); pending {
		t.Fatal("another profile is blocked")
	}
}

func TestPendingRevocationsIndex(t *testing.T) {
	dir := tempDir(t)
	store := newMemoryStore()
	pending, _ := NewFilePendingRevocations(dir, testProfileID, store)
	one := contracts.UnrevokedGrant{ClientID: "client_one", AccessToken: "access-1", RefreshToken: "refresh-1"}
	two := contracts.UnrevokedGrant{ClientID: "client_two", AccessToken: "access-2"}
	for _, g := range []contracts.UnrevokedGrant{one, one, two} {
		if err := pending.Add(g); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := pending.Entries()
	if err != nil || len(entries) != 2 || entries[0] != one || entries[1] != two {
		t.Fatalf("entries: %+v %v", entries, err)
	}
	index := readFile(t, pending.Path)
	if strings.Contains(index, "access") || strings.Contains(index, "refresh") {
		t.Fatalf("tokens leaked into the index: %s", index)
	}
	if len(store.values) != 2 {
		t.Fatalf("store: %v", store.values)
	}
	// A failed token write leaves the index as it was.
	store.failWrite = true
	if err := pending.Add(contracts.UnrevokedGrant{ClientID: "c3", AccessToken: "access-3"}); err == nil {
		t.Fatal("want the store failure")
	}
	store.failWrite = false
	if readFile(t, pending.Path) != index {
		t.Fatal("index not rolled back")
	}
	// An entry whose tokens are unreadable is never returned and is forgotten on replace.
	for key := range store.values {
		if strings.Contains(store.values[key], "access-2") {
			store.values[key] = "{not json"
		}
	}
	if entries, _ := pending.Entries(); len(entries) != 1 {
		t.Fatalf("unreadable tokens returned: %+v", entries)
	}
	if err := pending.Replace([]contracts.UnrevokedGrant{one}); err != nil {
		t.Fatal(err)
	}
	if len(store.values) != 1 || !strings.Contains(readFile(t, pending.Path), "client_one") || strings.Contains(readFile(t, pending.Path), "client_two") {
		t.Fatalf("replace kept the wrong grants: %v %s", store.values, readFile(t, pending.Path))
	}
	if err := pending.Replace(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(pending.Path); !os.IsNotExist(err) || len(store.values) != 0 {
		t.Fatal("an empty list keeps no index or tokens")
	}
}

func TestPendingRevocationsNeverOverwriteAnUnreadableIndex(t *testing.T) {
	dir := tempDir(t)
	pending, _ := NewFilePendingRevocations(dir, testProfileID, newMemoryStore())
	grant := contracts.UnrevokedGrant{ClientID: "c", AccessToken: "a"}
	writeFile(t, pending.Path, `{"grants":[{"id":"x"}]}`)
	_, err := pending.Entries()
	wantCliError(t, err, "Move that file aside")
	wantCliError(t, pending.Add(grant), "Move that file aside")
	if readFile(t, pending.Path) != `{"grants":[{"id":"x"}]}` {
		t.Fatal("unreadable index overwritten")
	}
	if runtime.GOOS == "windows" {
		return
	}
	_ = os.Remove(pending.Path)
	_ = os.Symlink(filepath.Join(dir, "nowhere"), pending.Path)
	wantCliError(t, pending.Add(grant), "Move that file aside")
	if info, err := os.Lstat(pending.Path); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("dangling symlink replaced")
	}
}
