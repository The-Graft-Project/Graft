package ssh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestMain lets the test binary stand in for graft as the SSH_ASKPASS helper
// when an external ssh launched by a test calls back into it.
func TestMain(m *testing.M) {
	RunAskpassIfRequested()
	os.Exit(m.Run())
}

const testPassphrase = "correct horse"

// newEncryptedKey returns a passphrase-protected ed25519 key in OpenSSH PEM
// form, plus the public key it belongs to.
func newEncryptedKey(t *testing.T, passphrase string) ([]byte, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return pem.EncodeToMemory(block), sshPub
}

// stubPassphrase replaces the terminal prompt with answers, returning a
// counter of how many times graft asked. It also empties the unlock cache so
// tests do not see each other's keys.
func stubPassphrase(t *testing.T, answers ...string) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	orig := readPassphrase
	readPassphrase = func(string) ([]byte, error) {
		n := int(calls.Add(1))
		if n > len(answers) {
			return nil, errors.New("no more answers")
		}
		return []byte(answers[n-1]), nil
	}
	resetUnlockCache()
	t.Cleanup(func() {
		readPassphrase = orig
		resetUnlockCache()
	})
	return &calls
}

func writeKey(t *testing.T, keyPEM []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "id_test")
	// replaceFile restricts the ACL on Windows, which OpenSSH insists on.
	if err := replaceFile(path, keyPEM, 0600, true); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

func TestUnencryptedKeyNeverAsks(t *testing.T) {
	calls := stubPassphrase(t)
	_, keyPEM := newTestSigner(t)

	k, err := unlockKey(keyPEM, "test key")
	if err != nil {
		t.Fatalf("unlockKey: %v", err)
	}
	if k.passphrase != nil {
		t.Fatalf("unencrypted key reported a passphrase")
	}
	if calls.Load() != 0 {
		t.Fatalf("asked for a passphrase %d times, want 0", calls.Load())
	}
}

func TestEncryptedKeyAsksOncePerProcess(t *testing.T) {
	calls := stubPassphrase(t, testPassphrase)
	keyPEM, pub := newEncryptedKey(t, testPassphrase)

	for i := 0; i < 3; i++ {
		k, err := unlockKey(keyPEM, "test key")
		if err != nil {
			t.Fatalf("unlockKey #%d: %v", i+1, err)
		}
		if !bytes.Equal(k.signer.PublicKey().Marshal(), pub.Marshal()) {
			t.Fatalf("signer is for the wrong key")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("asked for the passphrase %d times, want 1", calls.Load())
	}
}

func TestWrongPassphraseIsRetried(t *testing.T) {
	calls := stubPassphrase(t, "nope", testPassphrase)
	keyPEM, _ := newEncryptedKey(t, testPassphrase)

	if _, err := unlockKey(keyPEM, "test key"); err != nil {
		t.Fatalf("unlockKey: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("asked %d times, want 2", calls.Load())
	}
}

func TestWrongPassphraseGivesUpAndIsNotCached(t *testing.T) {
	calls := stubPassphrase(t, "a", "b", "c", testPassphrase)
	keyPEM, _ := newEncryptedKey(t, testPassphrase)

	_, err := unlockKey(keyPEM, "test key")
	if err == nil || !strings.Contains(err.Error(), "incorrect passphrase") {
		t.Fatalf("err = %v, want incorrect passphrase", err)
	}
	if calls.Load() != maxPassphraseAttempts {
		t.Fatalf("asked %d times, want %d", calls.Load(), maxPassphraseAttempts)
	}

	// A failed unlock must not poison the cache: the next attempt asks again.
	if _, err := unlockKey(keyPEM, "test key"); err != nil {
		t.Fatalf("second unlockKey: %v", err)
	}
}

func TestNoTerminalExplainsWhy(t *testing.T) {
	orig := readPassphrase
	readPassphrase = func(string) ([]byte, error) { return nil, errNoTerminal }
	resetUnlockCache()
	t.Cleanup(func() { readPassphrase = orig; resetUnlockCache() })

	keyPEM, _ := newEncryptedKey(t, testPassphrase)
	_, err := unlockKey(keyPEM, "/keys/id_test")
	if err == nil || !strings.Contains(err.Error(), "passphrase protected") || !strings.Contains(err.Error(), "/keys/id_test") {
		t.Fatalf("err = %v, want it to name the key and say it is passphrase protected", err)
	}
}

func TestGarbageKeyIsNotAPassphraseProblem(t *testing.T) {
	calls := stubPassphrase(t)
	_, err := unlockKey([]byte("not a key"), "test key")
	if err == nil || !strings.Contains(err.Error(), "unable to parse private key") {
		t.Fatalf("err = %v, want parse error", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("asked for a passphrase for a non-key")
	}
}

func TestDialUnlocksOnceAcrossReconnects(t *testing.T) {
	calls := stubPassphrase(t, testPassphrase)
	srv := startTestSSHServer(t)
	c := newTestClient(t, srv.addr)
	keyPEM, _ := newEncryptedKey(t, testPassphrase)
	if err := os.WriteFile(c.keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		client, err := c.dial()
		if err != nil {
			t.Fatalf("dial #%d: %v", i+1, err)
		}
		client.Close()
	}
	if calls.Load() != 1 {
		t.Fatalf("asked for the passphrase %d times across 3 connections, want 1", calls.Load())
	}
}

func TestAskpassEnvOnlyForEncryptedKeys(t *testing.T) {
	stubPassphrase(t, testPassphrase)

	_, plainPEM := newTestSigner(t)
	env, err := askpassEnv(writeKey(t, plainPEM), false)
	if err != nil || env != nil {
		t.Fatalf("unencrypted key: env=%v err=%v, want none", env, err)
	}

	encPEM, _ := newEncryptedKey(t, testPassphrase)
	env, err = askpassEnv(writeKey(t, encPEM), true)
	if err != nil {
		t.Fatalf("askpassEnv: %v", err)
	}
	got := strings.Join(env, "\n")
	for _, want := range []string{"SSH_ASKPASS_REQUIRE=force", envAskpassPassphrase + "=" + testPassphrase, "WSLENV="} {
		if !strings.Contains(got, want) {
			t.Errorf("env missing %q:\n%s", want, got)
		}
	}
}

func TestAskpassAnswersOnlyPassphrasePrompts(t *testing.T) {
	cases := []struct {
		prompt string
		ok     bool
	}{
		{`Enter passphrase for key 'C:\Users\me\.ssh\id_ed25519': `, true},
		{`Enter passphrase for "/home/me/.ssh/graft_key.pem": `, true},
		// A server falling back to password auth must never receive the key's
		// passphrase.
		{"me@203.0.113.5's password: ", false},
		{"Are you sure you want to continue connecting (yes/no/[fingerprint])? ", false},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		code := askpass([]string{tc.prompt}, testPassphrase, &out)
		if tc.ok && (code != 0 || strings.TrimSpace(out.String()) != testPassphrase) {
			t.Errorf("%q: code=%d out=%q, want the passphrase", tc.prompt, code, out.String())
		}
		if !tc.ok && (code == 0 || out.Len() != 0) {
			t.Errorf("%q: code=%d out=%q, want a refusal", tc.prompt, code, out.String())
		}
	}
}

// TestExternalSSHUsesCachedPassphrase runs the real ssh binary against an
// in-process server with an encrypted key. The test binary acts as the askpass
// helper (see TestMain), so a successful login proves the external ssh got the
// passphrase graft already holds, without asking the user a second time.
func TestExternalSSHUsesCachedPassphrase(t *testing.T) {
	sshCmd, isWSL := findSSH()
	if sshCmd == "" || isWSL {
		t.Skip("no native ssh binary")
	}
	calls := stubPassphrase(t, testPassphrase)

	keyPEM, pub := newEncryptedKey(t, testPassphrase)
	keyPath := writeKey(t, keyPEM)

	var authed atomic.Int32
	hostSigner, _ := newTestSigner(t)
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(k.Marshal(), pub.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("unknown key")
		},
	}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					conn.Close()
					return
				}
				authed.Add(1)
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					ch.Reject(ssh.Prohibited, "test server")
				}
				sc.Close()
			}()
		}
	}()

	// Unlock in-process first, as a graft command does before it shells out.
	if _, err := unlockKeyFile(keyPath); err != nil {
		t.Fatalf("unlockKeyFile: %v", err)
	}
	env, err := askpassEnv(keyPath, false)
	if err != nil {
		t.Fatalf("askpassEnv: %v", err)
	}

	dir := t.TempDir()
	emptyConfig := filepath.Join(dir, "config")
	os.WriteFile(emptyConfig, nil, 0600)
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	cmd := exec.Command(sshCmd,
		"-F", emptyConfig,
		"-i", keyPath,
		"-p", port,
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile="+filepath.Join(dir, "known_hosts"),
		"-o", "PreferredAuthentications=publickey",
		"tester@127.0.0.1", "true")
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Run() // fails at the channel step by design; auth is what we check

	if authed.Load() == 0 {
		t.Fatalf("external ssh never authenticated with the encrypted key; stderr:\n%s", stderr.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("user was asked %d times, want 1", calls.Load())
	}
}
