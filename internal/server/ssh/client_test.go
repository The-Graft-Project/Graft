package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// newTestSigner returns a throwaway ed25519 signer plus its PEM encoding.
func newTestSigner(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer, pem.EncodeToMemory(block)
}

// testSSHServer is a minimal in-process sshd that accepts any public key,
// answers global requests, and rejects channels.
type testSSHServer struct {
	addr string
	ln   net.Listener
}

func startTestSSHServer(t *testing.T) *testSSHServer {
	t.Helper()
	hostSigner, _ := newTestSigner(t)

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &testSSHServer{addr: ln.Addr().String(), ln: ln}

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					c.Close()
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					ch.Reject(ssh.Prohibited, "no channels in test server")
				}
			}(c)
		}
	}()

	t.Cleanup(func() { ln.Close() })
	return s
}

// freezableProxy forwards TCP to target until frozen, after which bytes are
// swallowed but sockets stay open - a NAT/wifi black hole.
type freezableProxy struct {
	addr   string
	frozen atomic.Bool
	ln     net.Listener
}

func startFreezableProxy(t *testing.T, target string) *freezableProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	p := &freezableProxy{addr: ln.Addr().String(), ln: ln}

	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 && !p.frozen.Load() {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			go pipe(up, c)
			go pipe(c, up)
		}
	}()

	t.Cleanup(func() { ln.Close() })
	return p
}

// newTestClient builds a Client wired to addr with a throwaway key and an
// isolated known_hosts, without touching the user's real ~/.ssh.
func newTestClient(t *testing.T, addr string) *Client {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	_, keyPEM := newTestSigner(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_test")
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	return &Client{
		host:           host,
		port:           port,
		user:           "tester",
		keyPath:        keyPath,
		knownHostsPath: filepath.Join(dir, "known_hosts"),
	}
}

// --- the bugs under test -------------------------------------------------

// isAlive() is a liveness probe, so it must answer within a bounded time even
// when the connection is a black hole. Before the fix it blocked forever.
func TestIsAliveReturnsFalseOnBlackHoledConnection(t *testing.T) {
	srv := startTestSSHServer(t)
	proxy := startFreezableProxy(t, srv.addr)

	c := newTestClient(t, proxy.addr)
	client, err := c.dial()
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	c.client = client

	if !c.isAlive() {
		t.Fatal("expected healthy connection to report alive")
	}

	proxy.frozen.Store(true)

	done := make(chan bool, 1)
	go func() { done <- c.isAlive() }()

	select {
	case alive := <-done:
		if alive {
			t.Fatal("black-holed connection reported as alive")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("isAlive() blocked >20s on a dead connection: probe has no timeout")
	}
}

// A second caller must not queue forever behind a stuck probe (globalSentMu).
func TestIsAliveDoesNotBlockConcurrentCallers(t *testing.T) {
	srv := startTestSSHServer(t)
	proxy := startFreezableProxy(t, srv.addr)

	c := newTestClient(t, proxy.addr)
	client, err := c.dial()
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	c.client = client
	proxy.frozen.Store(true)

	done := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- c.isAlive() }()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(25 * time.Second):
			t.Fatal("concurrent isAlive() callers blocked >25s behind globalSentMu")
		}
	}
}

// ClientConfig.Timeout only covers net.DialTimeout; a peer that accepts TCP
// and then never speaks SSH must still fail within a bounded time.
func TestDialFailsFastWhenHandshakeStalls(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Accept and then say nothing, holding the socket open.
			go func(c net.Conn) { time.Sleep(2 * time.Minute); c.Close() }(c)
		}
	}()

	c := newTestClient(t, ln.Addr().String())

	done := make(chan error, 1)
	go func() {
		cl, err := c.dial()
		if cl != nil {
			cl.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected handshake to fail against a silent peer")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("dial() blocked >30s on a stalled handshake: no deadline around the handshake")
	}
}

// A host on a non-standard port must be written in the format the lookup uses,
// otherwise every connection re-appends a duplicate entry.
func TestKnownHostsEntryMatchesLookupForNonStandardPort(t *testing.T) {
	dir := t.TempDir()
	khPath := filepath.Join(dir, "known_hosts")

	hostSigner, _ := newTestSigner(t)
	hostKey := hostSigner.PublicKey()
	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}

	cb := createHostKeyCallback(khPath, "example.test", 2222)

	// First connection: host unknown, gets recorded.
	if err := cb("example.test:2222", addr, hostKey); err != nil {
		t.Fatalf("first callback: %v", err)
	}
	after1, _ := os.ReadFile(khPath)

	// Second connection: must match what was written, and write nothing new.
	if err := cb("example.test:2222", addr, hostKey); err != nil {
		t.Fatalf("second callback: %v", err)
	}
	after2, _ := os.ReadFile(khPath)

	if len(after2) != len(after1) {
		t.Fatalf("known_hosts grew on a repeat connection (duplicate entry):\nfirst:\n%s\nsecond:\n%s", after1, after2)
	}

	kh, err := knownhosts.New(khPath)
	if err != nil {
		t.Fatalf("knownhosts.New: %v", err)
	}
	if err := kh("example.test:2222", addr, hostKey); err != nil {
		t.Fatalf("written entry does not satisfy its own lookup: %v", err)
	}
}

// A corrupt known_hosts must not silently disable host key verification.
func TestCorruptKnownHostsDoesNotSkipVerification(t *testing.T) {
	dir := t.TempDir()
	khPath := filepath.Join(dir, "known_hosts")

	goodSigner, _ := newTestSigner(t)
	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}

	// Record the real host key, then corrupt the file.
	cb := createHostKeyCallback(khPath, "example.test", 22)
	if err := cb("example.test:22", addr, goodSigner.PublicKey()); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := os.WriteFile(khPath, []byte("this is not a known_hosts line\n"), 0600); err != nil {
		t.Fatalf("corrupt: %v", err)
	}

	// An attacker's key must not be silently accepted just because the file
	// failed to parse.
	evilSigner, _ := newTestSigner(t)
	cb2 := createHostKeyCallback(khPath, "example.test", 22)
	if err := cb2("example.test:22", addr, evilSigner.PublicKey()); err == nil {
		t.Fatal("unparseable known_hosts silently accepted an unverified host key")
	}
}
