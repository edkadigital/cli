package credential

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestFileStorePermissionsAndDeletion(t *testing.T) {
	store := Store{Dir: t.TempDir(), Mode: "file"}
	if _, err := store.Save("../escape", &Session{}); err == nil {
		t.Fatal("accepted escaping profile")
	}
	_, err := store.Save("default", &Session{AccessToken: "secret", RefreshToken: "refresh"})
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(store.Dir, "credentials", "default.json"))
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("credentials not private")
	}
	session, err := store.Load("default")
	if err != nil || session.AccessToken != "secret" {
		t.Fatal("store roundtrip")
	}
	if err := store.Delete("default"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("default"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

// workingKeyring replaces the system keyring with one in memory, so a test
// never reads or writes the keyring of the machine it runs on.
func workingKeyring(t *testing.T) {
	t.Helper()
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
}

// brokenKeyring is a keyring that fails every call, as a machine without a
// keyring service does.
func brokenKeyring(t *testing.T) {
	t.Helper()
	keyring.MockInitWithError(errors.New("no keyring service"))
	t.Cleanup(keyring.MockInit)
}

func credentialFile(store Store, profile string) string {
	return filepath.Join(store.Dir, "credentials", profile+".json")
}

func TestKeyringStoreKeepsCredentialsOutOfFiles(t *testing.T) {
	workingKeyring(t)
	for _, mode := range []string{"keyring", "auto"} {
		store := Store{Dir: t.TempDir(), Mode: mode}
		location, err := store.Save("default", &Session{AccessToken: "access", RefreshToken: "refresh"})
		if err != nil || location != "keyring" {
			t.Fatal(mode, location, err)
		}
		if _, err := os.Stat(credentialFile(store, "default")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s wrote a credential file: %v", mode, err)
		}
		session, err := store.Load("default")
		if err != nil || session.AccessToken != "access" || session.RefreshToken != "refresh" {
			t.Fatal(mode, session, err)
		}
		// Another profile, and another configuration directory, see nothing.
		if _, err := store.Load("work"); !errors.Is(err, ErrNotFound) {
			t.Fatal(mode, err)
		}
		if _, err := (Store{Dir: t.TempDir(), Mode: mode}).Load("default"); !errors.Is(err, ErrNotFound) {
			t.Fatal(mode, err)
		}
		if err := store.Delete("default"); err != nil {
			t.Fatal(mode, err)
		}
		if _, err := store.Load("default"); !errors.Is(err, ErrNotFound) {
			t.Fatal(mode, err)
		}
		// Deleting what is not there is not an error.
		if err := store.Delete("default"); err != nil {
			t.Fatal(mode, err)
		}
	}
}

func TestAutoStoreFallsBackToAPrivateFile(t *testing.T) {
	brokenKeyring(t)
	store := Store{Dir: t.TempDir(), Mode: "auto"}
	location, err := store.Save("default", &Session{AccessToken: "access"})
	if err != nil || location != "file" {
		t.Fatal(location, err)
	}
	info, err := os.Stat(credentialFile(store, "default"))
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	if session, err := store.Load("default"); err != nil || session.AccessToken != "access" {
		t.Fatal(session, err)
	}
	if err := store.Delete("default"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("default"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// With no file to remove either, the keyring's failure is the answer.
	if err := store.Delete("default"); err == nil || !strings.Contains(err.Error(), "remove credentials from system keyring") {
		t.Fatal(err)
	}
}

// SignedIn finds credentials in a file or the keyring. A keyring it can't read
// is an error, not an absence, unless a file answers first or file mode leaves
// the keyring out.
func TestSignedIn(t *testing.T) {
	workingKeyring(t)
	for _, mode := range []string{"keyring", "auto"} {
		store := Store{Dir: t.TempDir(), Mode: mode}
		if in, err := store.SignedIn("default"); err != nil || in {
			t.Fatal(mode, in, err)
		}
		if _, err := store.Save("default", &Session{AccessToken: "access"}); err != nil {
			t.Fatal(mode, err)
		}
		if in, err := store.SignedIn("default"); err != nil || !in {
			t.Fatal(mode, in, err)
		}
	}
	brokenKeyring(t)
	for _, mode := range []string{"keyring", "auto"} {
		store := Store{Dir: t.TempDir(), Mode: mode}
		if _, err := store.SignedIn("default"); err == nil || !strings.Contains(err.Error(), "read system keyring") {
			t.Fatal(mode, err)
		}
		if _, err := (Store{Dir: store.Dir, Mode: "file"}).Save("default", &Session{AccessToken: "access"}); err != nil {
			t.Fatal(mode, err)
		}
		if in, err := store.SignedIn("default"); err != nil || !in {
			t.Fatal(mode, in, err)
		}
	}
	if in, err := (Store{Dir: t.TempDir(), Mode: "file"}).SignedIn("default"); err != nil || in {
		t.Fatal(in, err)
	}
	if _, err := (Store{Dir: t.TempDir(), Mode: "file"}).SignedIn("../escape"); err == nil || !strings.Contains(err.Error(), "invalid profile name") {
		t.Fatal(err)
	}
}

func TestKeyringStoreNeverFallsBackToAFile(t *testing.T) {
	brokenKeyring(t)
	store := Store{Dir: t.TempDir(), Mode: "keyring"}
	if _, err := store.Save("default", &Session{AccessToken: "access"}); err == nil || !strings.Contains(err.Error(), "save to system keyring") {
		t.Fatal(err)
	}
	if _, err := os.Stat(credentialFile(store, "default")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("keyring mode wrote a credential file: %v", err)
	}
	if _, err := store.Load("default"); err == nil || !strings.Contains(err.Error(), "read system keyring") {
		t.Fatal(err)
	}
	if err := store.Delete("default"); err == nil || !strings.Contains(err.Error(), "remove credentials from system keyring") {
		t.Fatal(err)
	}
}

// A file written while the keyring was unavailable is the newer copy. The next
// save to the keyring removes it, so one copy is left.
func TestAutoStoreMovesAFileIntoTheKeyring(t *testing.T) {
	workingKeyring(t)
	dir := t.TempDir()
	if _, err := (Store{Dir: dir, Mode: "keyring"}).Save("default", &Session{AccessToken: "in-keyring"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{Dir: dir, Mode: "file"}).Save("default", &Session{AccessToken: "in-file"}); err != nil {
		t.Fatal(err)
	}
	store := Store{Dir: dir, Mode: "auto"}
	if session, err := store.Load("default"); err != nil || session.AccessToken != "in-file" {
		t.Fatal(session, err)
	}
	if location, err := store.Save("default", &Session{AccessToken: "refreshed"}); err != nil || location != "keyring" {
		t.Fatal(location, err)
	}
	if _, err := os.Stat(credentialFile(store, "default")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the file copy stayed: %v", err)
	}
	if session, err := store.Load("default"); err != nil || session.AccessToken != "refreshed" {
		t.Fatal(session, err)
	}
	// Deleting removes the keyring copy and a file copy alike.
	if _, err := (Store{Dir: dir, Mode: "file"}).Save("default", &Session{AccessToken: "in-file"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete("default"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"keyring", "file", "auto"} {
		if _, err := (Store{Dir: dir, Mode: mode}).Load("default"); !errors.Is(err, ErrNotFound) {
			t.Fatal(mode, err)
		}
	}
}

func TestStoreRejectsDamagedCredentialsAndUnsafeProfiles(t *testing.T) {
	workingKeyring(t)
	store := Store{Dir: t.TempDir(), Mode: "file"}
	if err := os.MkdirAll(filepath.Dir(credentialFile(store, "default")), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialFile(store, "default"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("default"); err == nil || !strings.Contains(err.Error(), "invalid saved credentials") {
		t.Fatal(err)
	}
	inKeyring := Store{Dir: t.TempDir(), Mode: "keyring"}
	if err := keyring.Set("edka-cli", inKeyring.Dir+":default", "{not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := inKeyring.Load("default"); err == nil {
		t.Fatal("loaded a damaged keyring entry")
	}
	// A profile names a file and a keyring entry, so it holds no path.
	for _, profile := range []string{"../escape", "a/b", "", "with space"} {
		if _, err := store.Load(profile); err == nil || !strings.Contains(err.Error(), "invalid profile name") {
			t.Errorf("Load(%q): %v", profile, err)
		}
		if err := store.Delete(profile); err == nil || !strings.Contains(err.Error(), "invalid profile name") {
			t.Errorf("Delete(%q): %v", profile, err)
		}
		if _, err := inKeyring.Save(profile, &Session{}); err == nil || !strings.Contains(err.Error(), "invalid profile name") {
			t.Errorf("Save(%q): %v", profile, err)
		}
	}
}
