package ssh

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/crypto/ssh"
)

// remoteAuthorizedKeys is the shell expression for the file graft edits on a
// server during a key rollout.
const remoteAuthorizedKeys = `"$HOME/.ssh/authorized_keys"`

// authorizedKeyLine parses an authorized_keys-format public key and returns
// its canonical "type base64" line plus the base64 body alone. Both are
// rebuilt from the parsed key, so they contain only [A-Za-z0-9+/=-] and a
// space and are safe to embed in single-quoted shell strings.
func authorizedKeyLine(pub string) (line, body string, err error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		return "", "", fmt.Errorf("invalid public key: %v", err)
	}
	line = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	body = base64.StdEncoding.EncodeToString(key.Marshal())
	return line, body, nil
}

// PublicKeyFromPrivate returns the authorized_keys line for a PEM private key.
// Deriving it from the private key, rather than trusting a separate .pub file,
// guarantees it names the key graft actually authenticates with.
func PublicKeyFromPrivate(privPEM []byte) (string, error) {
	signer, err := ssh.ParsePrivateKey(privPEM)
	if err != nil {
		return "", fmt.Errorf("unable to parse private key: %v", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
}

// Keys are matched with grep -F (fixed strings): a base64 body contains '/'
// and '+', which break any pattern-based match such as sed addresses.

// addAuthorizedKeyScript appends newLine to file unless its key is already
// there. It refuses when oldBody is absent, because then file is not where
// sshd found the key graft just logged in with, and editing it would achieve
// nothing.
func addAuthorizedKeyScript(file, oldBody, newLine string) string {
	newBody := strings.Fields(newLine)[1]
	return fmt.Sprintf(`F=%s
grep -qF -- '%s' "$F" 2>/dev/null || { echo "current key not found in $F" >&2; exit 1; }
grep -qF -- '%s' "$F" && exit 0
if [ -n "$(tail -c 1 "$F")" ]; then echo >> "$F" || exit 1; fi
printf '%%s\n' '%s' >> "$F"
`, file, oldBody, newBody, newLine)
}

// removeAuthorizedKeyScript deletes every line of file holding removeBody. It
// refuses unless keepBody is present, so it can never leave the server with no
// graft key. The file is rewritten in place (not replaced) to keep its owner,
// mode and SELinux label.
func removeAuthorizedKeyScript(file, removeBody, keepBody string) string {
	return fmt.Sprintf(`F=%s
grep -qF -- '%s' "$F" || { echo "replacement key not found in $F; refusing to remove the other key" >&2; exit 1; }
T=$(mktemp "$F.XXXXXX") || exit 1
rc=0
grep -vF -- '%s' "$F" > "$T" || rc=$?
if [ "$rc" -gt 1 ]; then rm -f "$T"; exit "$rc"; fi
cat "$T" > "$F" || { rm -f "$T"; exit 1; }
rm -f "$T"
`, file, keepBody, removeBody)
}

// AddAuthorizedKey authorizes newPub on the server alongside oldPub, the key
// this client is logged in with. Running it twice does not duplicate the key.
func (c *Client) AddAuthorizedKey(oldPub, newPub string) error {
	_, oldBody, err := authorizedKeyLine(oldPub)
	if err != nil {
		return err
	}
	newLine, _, err := authorizedKeyLine(newPub)
	if err != nil {
		return err
	}
	return c.runScript(addAuthorizedKeyScript(remoteAuthorizedKeys, oldBody, newLine))
}

// RemoveAuthorizedKey revokes removePub on the server, refusing unless keepPub
// remains authorized.
func (c *Client) RemoveAuthorizedKey(removePub, keepPub string) error {
	_, removeBody, err := authorizedKeyLine(removePub)
	if err != nil {
		return err
	}
	_, keepBody, err := authorizedKeyLine(keepPub)
	if err != nil {
		return err
	}
	return c.runScript(removeAuthorizedKeyScript(remoteAuthorizedKeys, removeBody, keepBody))
}

// runScript runs a POSIX script through sh (whatever the login shell is) and
// folds its stderr into the error, so a failure says why instead of only
// "Process exited with status 1".
func (c *Client) runScript(script string) error {
	var stderr bytes.Buffer
	if err := c.RunCommand("sh -c "+shellQuote(script), nil, &stderr); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%v: %s", err, msg)
		}
		return err
	}
	return nil
}

// VerifyKeyLogin opens a fresh connection authenticated only by keyPEM and
// runs a no-op, proving the server accepts that key.
func VerifyKeyLogin(host string, port int, user string, keyPEM []byte) error {
	c := &Client{host: host, port: port, user: user, keyPEM: keyPEM}
	client, err := c.dial()
	if err != nil {
		return err
	}
	c.client = client
	defer c.Close()
	return c.RunCommand("true", nil, nil)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ReplaceKeyPair swaps new key material into pemPath and pubPath. Each file is
// written to a temp file beside it and renamed over the original, so a
// half-written key never exists, and an original locked read-only (as OpenSSH
// on Windows demands) does not block the update.
func ReplaceKeyPair(pemPath, pubPath, privPEM, pub string) error {
	if err := replaceFile(pemPath, []byte(privPEM), 0600, true); err != nil {
		return fmt.Errorf("private key: %w", err)
	}
	if err := replaceFile(pubPath, []byte(pub), 0644, false); err != nil {
		return fmt.Errorf("public key: %w", err)
	}
	return nil
}

func replaceFile(path string, data []byte, perm os.FileMode, private bool) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once renamed

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	if private && runtime.GOOS == "windows" {
		if err := restrictToOwner(tmp); err != nil {
			return err
		}
	}
	return os.Rename(tmp, path)
}

// restrictToOwner drops inherited ACL entries so only the current user can
// read the file. Windows OpenSSH rejects private keys that other accounts can
// read, and a temp file inherits the directory's broader ACL.
func restrictToOwner(path string) error {
	owner, err := ownerAccount()
	if err != nil {
		return err
	}
	out, err := exec.Command("icacls", path, "/inheritance:r", "/grant:r", owner+":(F)").CombinedOutput()
	if err != nil {
		return fmt.Errorf("restricting permissions: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ownerAccount() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("unable to determine current user: %v", err)
	}
	return u.Username, nil
}
