package ssh

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testPubKey returns an authorized_keys line and its base64 body. With
// wantSlash it keeps generating until the body contains '/', the character
// that broke the old sed-based rollout.
func testPubKey(t *testing.T, wantSlash bool) (line, body string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		signer, _ := newTestSigner(t)
		line, body, err := authorizedKeyLine(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
		if err != nil {
			t.Fatalf("authorizedKeyLine: %v", err)
		}
		if !wantSlash || strings.Contains(body, "/") {
			return line, body
		}
	}
	t.Fatal("could not generate a key containing '/'")
	return "", ""
}

// runScript runs a generated script against a local authorized_keys file,
// standing in for the remote host's sh.
func runScript(t *testing.T, script string) (string, error) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	out, err := exec.Command(sh, "-c", script).CombinedOutput()
	return string(out), err
}

func newKeysFile(t *testing.T, content string) (path, shellExpr string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "authorized_keys")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path, shellQuote(filepath.ToSlash(path))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

func TestAddAuthorizedKeyHandlesSlashesAndMissingNewline(t *testing.T) {
	oldLine, oldBody := testPubKey(t, true)
	newLine, _ := testPubKey(t, true)
	otherLine, _ := testPubKey(t, false)

	// No trailing newline: a naive append would glue the new key onto the old.
	path, f := newKeysFile(t, otherLine+"\n"+oldLine+" me@laptop")

	if out, err := runScript(t, addAuthorizedKeyScript(f, oldBody, newLine)); err != nil {
		t.Fatalf("add failed: %v: %s", err, out)
	}
	want := otherLine + "\n" + oldLine + " me@laptop\n" + newLine + "\n"
	if got := readFile(t, path); got != want {
		t.Fatalf("file after add:\n%q\nwant:\n%q", got, want)
	}

	// Re-running must not add a second copy.
	if out, err := runScript(t, addAuthorizedKeyScript(f, oldBody, newLine)); err != nil {
		t.Fatalf("second add failed: %v: %s", err, out)
	}
	if got := readFile(t, path); got != want {
		t.Fatalf("file after second add:\n%q\nwant:\n%q", got, want)
	}
}

func TestAddAuthorizedKeyRefusesWhenOldKeyIsNotInFile(t *testing.T) {
	_, oldBody := testPubKey(t, false)
	newLine, _ := testPubKey(t, false)
	otherLine, _ := testPubKey(t, false)
	path, f := newKeysFile(t, otherLine+"\n")

	out, err := runScript(t, addAuthorizedKeyScript(f, oldBody, newLine))
	if err == nil {
		t.Fatal("add succeeded although the current key is not in the file")
	}
	if !strings.Contains(out, "current key not found") {
		t.Fatalf("unhelpful error output: %q", out)
	}
	if got := readFile(t, path); got != otherLine+"\n" {
		t.Fatalf("file changed on refusal: %q", got)
	}
}

func TestRemoveAuthorizedKeyKeepsEverythingElse(t *testing.T) {
	oldLine, oldBody := testPubKey(t, true)
	newLine, newBody := testPubKey(t, true)
	otherLine, _ := testPubKey(t, false)
	path, f := newKeysFile(t, otherLine+"\n"+oldLine+" me@laptop\n"+newLine+"\n")

	if out, err := runScript(t, removeAuthorizedKeyScript(f, oldBody, newBody)); err != nil {
		t.Fatalf("remove failed: %v: %s", err, out)
	}
	want := otherLine + "\n" + newLine + "\n"
	if got := readFile(t, path); got != want {
		t.Fatalf("file after remove:\n%q\nwant:\n%q", got, want)
	}
}

func TestRemoveAuthorizedKeyRefusesWithoutTheKeptKey(t *testing.T) {
	oldLine, oldBody := testPubKey(t, false)
	_, newBody := testPubKey(t, false)
	path, f := newKeysFile(t, oldLine+"\n")

	out, err := runScript(t, removeAuthorizedKeyScript(f, oldBody, newBody))
	if err == nil {
		t.Fatal("remove succeeded although it would leave no working graft key")
	}
	if !strings.Contains(out, "replacement key not found") {
		t.Fatalf("unhelpful error output: %q", out)
	}
	if got := readFile(t, path); got != oldLine+"\n" {
		t.Fatalf("file changed on refusal: %q", got)
	}
}

func TestAuthorizedKeyLineRejectsGarbage(t *testing.T) {
	if _, _, err := authorizedKeyLine("ssh-rsa not'base64"); err == nil {
		t.Fatal("expected an error for a malformed key")
	}
}

func TestPublicKeyFromPrivateMatchesSigner(t *testing.T) {
	signer, keyPEM := newTestSigner(t)
	got, err := PublicKeyFromPrivate(keyPEM)
	if err != nil {
		t.Fatalf("PublicKeyFromPrivate: %v", err)
	}
	want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestReplaceKeyPairOverwritesReadOnlyPrivateKey(t *testing.T) {
	dir := t.TempDir()
	pemPath := filepath.Join(dir, "graftpem")
	pubPath := filepath.Join(dir, "graftpub")
	if err := os.WriteFile(pemPath, []byte("old private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pubPath, []byte("old public"), 0644); err != nil {
		t.Fatal(err)
	}
	// Reproduce the lockdown people apply for OpenSSH: owner may only read.
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("icacls", pemPath, "/inheritance:r", "/grant:r", currentUserForACL(t)+":(R)").CombinedOutput(); err != nil {
			t.Fatalf("icacls: %v: %s", err, out)
		}
	} else if err := os.Chmod(pemPath, 0400); err != nil {
		t.Fatal(err)
	}

	if err := ReplaceKeyPair(pemPath, pubPath, "new private", "new public"); err != nil {
		t.Fatalf("ReplaceKeyPair: %v", err)
	}
	if got := readFile(t, pemPath); got != "new private" {
		t.Fatalf("private key = %q", got)
	}
	if got := readFile(t, pubPath); got != "new public" {
		t.Fatalf("public key = %q", got)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(pemPath); fi.Mode().Perm() != 0600 {
			t.Fatalf("private key mode = %v", fi.Mode().Perm())
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp-*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func currentUserForACL(t *testing.T) string {
	t.Helper()
	name, err := ownerAccount()
	if err != nil {
		t.Fatalf("ownerAccount: %v", err)
	}
	return name
}
