package ssh

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// Passphrase-protected keys.
//
// Graft never stores a key's passphrase. The first time a command needs an
// encrypted key it asks on the terminal, then keeps the unlocked key in memory
// until the command exits, so a command that opens several connections (or
// reconnects after a network blip) asks only once.
//
// Some work runs through an external ssh (rsync for sync/pull, the interactive
// shell), which would ask for the passphrase again on its own. For those, graft
// sets itself as ssh's SSH_ASKPASS helper and hands the passphrase to that one
// child process through its environment; see askpassEnv and askpass.

const (
	maxPassphraseAttempts = 3

	// envAskpassMode marks a graft process started by ssh as its askpass
	// helper rather than by the user.
	envAskpassMode = "GRAFT_ASKPASS"
	// envAskpassPassphrase carries the passphrase to that helper.
	envAskpassPassphrase = "GRAFT_ASKPASS_PASSPHRASE"
)

var errNoTerminal = errors.New("no terminal to ask for the passphrase on")

// unlockedKey is a parsed private key. passphrase is nil when the key is not
// encrypted.
type unlockedKey struct {
	signer     ssh.Signer
	passphrase []byte
}

var (
	// unlockMu also serializes prompts, so concurrent connections wanting the
	// same key produce one question, not several interleaved ones.
	unlockMu    sync.Mutex
	unlockCache = map[[sha256.Size]byte]*unlockedKey{}

	// readPassphrase asks the user for a passphrase. Tests replace it.
	readPassphrase = readPassphraseFromTerminal
)

func resetUnlockCache() {
	unlockMu.Lock()
	defer unlockMu.Unlock()
	unlockCache = map[[sha256.Size]byte]*unlockedKey{}
}

// unlockKey parses keyPEM, asking for its passphrase if it has one. label names
// the key in the prompt and in errors. The cache is keyed by the key's content,
// so the same key used by several registries is asked for once.
func unlockKey(keyPEM []byte, label string) (*unlockedKey, error) {
	signer, err := ssh.ParsePrivateKey(keyPEM)
	if err == nil {
		return &unlockedKey{signer: signer}, nil
	}
	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) {
		return nil, fmt.Errorf("unable to parse private key: %v", err)
	}

	id := sha256.Sum256(keyPEM)
	unlockMu.Lock()
	defer unlockMu.Unlock()
	if k, ok := unlockCache[id]; ok {
		return k, nil
	}

	var lastErr error
	for attempt := 1; attempt <= maxPassphraseAttempts; attempt++ {
		passphrase, err := readPassphrase(fmt.Sprintf("🔐 Passphrase for %s: ", label))
		if err != nil {
			return nil, fmt.Errorf("private key %s is passphrase protected: %v", label, err)
		}
		signer, err := ssh.ParsePrivateKeyWithPassphrase(keyPEM, passphrase)
		if err == nil {
			k := &unlockedKey{signer: signer, passphrase: passphrase}
			unlockCache[id] = k
			return k, nil
		}
		lastErr = err
		if attempt < maxPassphraseAttempts {
			fmt.Fprintln(os.Stderr, "❌ Incorrect passphrase, try again.")
		}
	}
	return nil, fmt.Errorf("incorrect passphrase for %s after %d attempts (%v)", label, maxPassphraseAttempts, lastErr)
}

// unlockKeyFile is unlockKey for a key on disk.
func unlockKeyFile(keyPath string) (*unlockedKey, error) {
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read private key: %v", err)
	}
	return unlockKey(keyPEM, keyPath)
}

// CheckKeyFile confirms keyPath holds a private key graft can use, asking for
// its passphrase if it has one. It reports whether the key is passphrase
// protected. The passphrase is kept only for the rest of this command.
func CheckKeyFile(keyPath string) (encrypted bool, err error) {
	k, err := unlockKeyFile(keyPath)
	if err != nil {
		return false, err
	}
	return k.passphrase != nil, nil
}

func readPassphraseFromTerminal(prompt string) ([]byte, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, errNoTerminal
	}

	// ReadPassword turns echo off. If Ctrl+C killed the process mid-prompt the
	// user's terminal would be left without echo, so restore it first.
	if state, err := term.GetState(fd); err == nil {
		sig := make(chan os.Signal, 1)
		done := make(chan struct{})
		signal.Notify(sig, os.Interrupt)
		defer func() {
			signal.Stop(sig)
			close(done)
		}()
		go func() {
			select {
			case <-sig:
				term.Restore(fd, state)
				fmt.Fprintln(os.Stderr)
				os.Exit(130)
			case <-done:
			}
		}()
	}

	fmt.Fprint(os.Stderr, prompt)
	passphrase, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return passphrase, err
}

// askpassEnv returns the environment an external ssh (or rsync's ssh) needs to
// unlock keyPath without asking the user again, or nil when the key has no
// passphrase. wsl adds the WSLENV entries that carry the variables into WSL,
// translating the helper's path to its /mnt/... form.
func askpassEnv(keyPath string, wsl bool) ([]string, error) {
	k, err := unlockKeyFile(keyPath)
	if err != nil {
		return nil, err
	}
	if k.passphrase == nil {
		return nil, nil
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("unable to locate graft for ssh's passphrase prompt: %v", err)
	}

	env := []string{
		"SSH_ASKPASS=" + exe,
		// force: use the helper even with a terminal attached and no DISPLAY.
		"SSH_ASKPASS_REQUIRE=force",
		envAskpassMode + "=1",
		envAskpassPassphrase + "=" + string(k.passphrase),
	}
	if wsl {
		wslenv := "SSH_ASKPASS/p:SSH_ASKPASS_REQUIRE:" + envAskpassMode + ":" + envAskpassPassphrase
		if existing := os.Getenv("WSLENV"); existing != "" {
			wslenv = existing + ":" + wslenv
		}
		env = append(env, "WSLENV="+wslenv)
	}
	return env, nil
}

// withAskpass returns the environment for an external ssh/rsync using keyPath:
// the current environment, plus the askpass helper when the key is encrypted.
func withAskpass(keyPath string, wsl bool) ([]string, error) {
	env, err := askpassEnv(keyPath, wsl)
	if err != nil || env == nil {
		return nil, err // nil Env means "inherit", which is right for plain keys
	}
	return append(os.Environ(), env...), nil
}

// RunAskpassIfRequested handles graft being run by ssh as its SSH_ASKPASS
// helper: it answers the prompt and exits. It must be the first thing main
// does. For any ordinary invocation it returns and does nothing.
func RunAskpassIfRequested() {
	if os.Getenv(envAskpassMode) != "1" {
		return
	}
	os.Exit(askpass(os.Args[1:], os.Getenv(envAskpassPassphrase), os.Stdout))
}

// askpass prints passphrase if the prompt asks for a key passphrase. Anything
// else (a server password, a host key confirmation) is refused: if the server
// falls back to password auth, the key's passphrase must not be sent to it.
func askpass(args []string, passphrase string, out io.Writer) int {
	prompt := strings.ToLower(strings.Join(args, " "))
	if passphrase == "" || !strings.Contains(prompt, "passphrase") {
		return 1
	}
	fmt.Fprintln(out, passphrase)
	return 0
}
