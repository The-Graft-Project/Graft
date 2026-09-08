package ssh

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// Bounds for every network step that would otherwise block forever. A
// half-dead TCP connection (wifi blip, NAT idle drop, roaming) stays open but
// delivers nothing, so any unbounded read on it hangs for good.
const (
	// connectTimeout bounds the TCP connect.
	connectTimeout = 10 * time.Second
	// handshakeTimeout bounds the SSH handshake and authentication, which
	// ssh.ClientConfig.Timeout does not cover.
	handshakeTimeout = 20 * time.Second
	// keepaliveTimeout bounds the liveness probe. A probe that can hang is
	// not a liveness probe.
	keepaliveTimeout = 5 * time.Second
	// channelTimeout bounds opening a new channel (port-forward, sftp).
	channelTimeout = 15 * time.Second
)

type Client struct {
	client  *ssh.Client
	sftp    *sftp.Client
	host    string
	port    int
	user    string
	keyPath string

	// knownHostsPath overrides the default ~/.ssh/known_hosts. Empty means
	// the default; tests set it to stay out of the real home directory.
	knownHostsPath string

	mu sync.RWMutex
}

func NewClient(host string, port int, user, keyPath string) (*Client, error) {
	// Expand tilde (~) if present in keyPath
	actualKeyPath := keyPath
	if strings.HasPrefix(keyPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("unable to get home directory: %v", err)
		}
		actualKeyPath = filepath.Join(home, keyPath[2:])
	} else if keyPath == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("unable to get home directory: %v", err)
		}
		actualKeyPath = home
	}

	c := &Client{
		host:    host,
		port:    port,
		user:    user,
		keyPath: actualKeyPath,
	}

	client, err := c.dial()
	if err != nil {
		return nil, err
	}

	// The sftp subsystem is started lazily by sftpClient(). Most commands
	// (shell, tunnel, docker) never transfer files, and opening the channel
	// eagerly cost every invocation an extra round trip and an extra channel
	// against the server's MaxSessions.
	c.client = client

	return c, nil
}

// dial establishes a fresh SSH connection using the client's stored credentials.
// It does not touch c.client/c.sftp, so it is safe to call while the existing
// connection is still in use (e.g. to test reconnection before swapping over).
func (c *Client) dial() (*ssh.Client, error) {
	key, err := os.ReadFile(c.keyPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read private key: %v", err)
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("unable to parse private key: %v", err)
	}

	knownHostsPath := c.knownHostsPath
	if knownHostsPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("unable to get home directory: %v", err)
		}
		knownHostsPath = filepath.Join(homeDir, ".ssh", "known_hosts")
	}
	hostKeyCallback := createHostKeyCallback(knownHostsPath, c.host, c.port)

	config := &ssh.ClientConfig{
		User: c.user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCallback,
		Timeout:         connectTimeout,
	}

	addr := net.JoinHostPort(c.host, strconv.Itoa(c.port))

	// ssh.Dial would apply config.Timeout to the TCP connect only, leaving the
	// handshake and authentication unbounded: a peer that accepts TCP and then
	// stalls would hang here forever. Drive the two steps separately so a
	// deadline can cover the handshake as well.
	conn, err := net.DialTimeout("tcp", addr, connectTimeout)
	if err != nil {
		return nil, fmt.Errorf("unable to connect: %v", err)
	}

	if err := conn.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("unable to connect: %v", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("unable to connect: %v", err)
	}

	// The deadline covered the handshake only; leaving it set would break the
	// long-lived session traffic that follows.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		sshConn.Close()
		return nil, fmt.Errorf("unable to connect: %v", err)
	}

	return ssh.NewClient(sshConn, chans, reqs), nil
}

// sshClient returns the currently active SSH connection.
func (c *Client) sshClient() *ssh.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client
}

// sftpClient returns the sftp session, starting it on first use.
func (c *Client) sftpClient() (*sftp.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sftp != nil {
		return c.sftp, nil
	}
	if c.client == nil {
		return nil, fmt.Errorf("ssh connection is not established")
	}

	type result struct {
		client *sftp.Client
		err    error
	}
	ch := make(chan result, 1)
	client := c.client
	go func() {
		sc, err := sftp.NewClient(client)
		ch <- result{sc, err}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("unable to start sftp: %v", r.err)
		}
		c.sftp = r.client
		return r.client, nil
	case <-time.After(channelTimeout):
		// Reap the session if it turns up after we gave up on it.
		go func() {
			if r := <-ch; r.client != nil {
				r.client.Close()
			}
		}()
		return nil, fmt.Errorf("timed out starting sftp subsystem after %s", channelTimeout)
	}
}

// isAlive reports whether the active SSH connection is still usable. The probe
// is bounded: on a half-dead connection the underlying global request never
// gets a reply, and an unbounded probe would hang exactly when it is most
// needed. The abandoned goroutine finishes once the connection is closed.
func (c *Client) isAlive() bool {
	client := c.sshClient()
	if client == nil {
		return false
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := client.SendRequest("graft-keepalive@graft", true, nil)
		done <- err
	}()

	select {
	case err := <-done:
		return err == nil
	case <-time.After(keepaliveTimeout):
		return false
	}
}

// reconnect re-establishes the SSH connection, replacing the active one.
func (c *Client) reconnect() error {
	return c.reconnectFrom(c.sshClient())
}

// reconnectFrom replaces stale with a fresh connection. If the active
// connection is no longer stale someone else already healed it, so callers
// racing on the same dead connection produce one reconnect, not one each.
func (c *Client) reconnectFrom(stale *ssh.Client) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if stale != nil && c.client != nil && c.client != stale {
		return nil
	}

	newClient, err := c.dial()
	if err != nil {
		return err
	}

	if c.sftp != nil {
		// Bound to the old connection; drop it so sftpClient() rebuilds it
		// against the new one on next use.
		c.sftp.Close()
		c.sftp = nil
	}
	if c.client != nil {
		// Closing the old connection also releases any probe still parked on
		// it inside isAlive.
		c.client.Close()
	}
	c.client = newClient
	return nil
}

// healLoop periodically checks the SSH connection and reconnects with backoff
// if it has dropped, so long-running tunnels survive network blips.
func (c *Client) healLoop(stop <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if c.isAlive() {
				continue
			}
			fmt.Fprintln(os.Stderr, "\n⚠️  Connection lost, attempting to reconnect...")
			backoff := time.Second
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := c.reconnect(); err != nil {
					time.Sleep(backoff)
					if backoff < 30*time.Second {
						backoff *= 2
					}
					continue
				}
				fmt.Fprintln(os.Stderr, "✅ Reconnected.")
				break
			}
		}
	}
}

func (c *Client) RunCommand(cmd string, stdout, stderr io.Writer) error {
	client := c.sshClient()
	if client == nil {
		return fmt.Errorf("ssh connection is not established")
	}
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	session.Stdout = stdout
	session.Stderr = stderr
	return session.Run(cmd)
}

func (c *Client) UpdateAuthorizedKey(oldPubKey, newPubKey string) error {
	// Extract base64 part of the keys for more reliable matching
	oldParts := strings.Fields(oldPubKey)
	if len(oldParts) < 2 {
		return fmt.Errorf("invalid old public key format")
	}
	oldBase64 := oldParts[1]

	// Use sed to replace the line containing the old base64 string with the new full public key
	// We use | as delimiter to avoid issues with / in keys (though unusual)
	cmd := fmt.Sprintf("sed -i '/%s/c\\%s' ~/.ssh/authorized_keys", oldBase64, strings.TrimSpace(newPubKey))
	return c.RunCommand(cmd, nil, nil)
}

func (c *Client) GetCommandOutput(cmd string) (string, error) {
	client := c.sshClient()
	if client == nil {
		return "", fmt.Errorf("ssh connection is not established")
	}
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	out, err := session.Output(cmd)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (c *Client) InteractiveSession() error {
	// Verify key exists
	if _, err := os.Stat(c.keyPath); err != nil {
		return fmt.Errorf("ssh key not found: %s", c.keyPath)
	}

	// Find best ssh command
	sshCmd, isWSL := findSSH()

	// No ssh binary available anywhere (native, Windows OpenSSH, or WSL) - fall
	// back to a Go-native simulated session over the existing connection.
	if sshCmd == "" {
		fmt.Println("⚠️  No SSH client found. Using simulated terminal (fallback).")
		return c.SimulatedSession()
	}

	args := []string{}
	if isWSL {
		wslKeyPath := "~/.ssh/graft_key.pem"
		windowsKeyWSL := convertToUnixPath(c.keyPath, true)

		// Copy key to WSL filesystem and set proper permissions
		copyCmd := exec.Command("wsl", "bash", "-c",
			fmt.Sprintf("mkdir -p ~/.ssh && cp '%s' %s && chmod 600 %s",
				windowsKeyWSL, wslKeyPath, wslKeyPath))
		if err := copyCmd.Run(); err != nil {
			return fmt.Errorf("failed to copy SSH key to WSL: %v", err)
		}

		args = []string{"ssh", "-i", wslKeyPath, "-p", fmt.Sprintf("%d", c.port), "-o", "StrictHostKeyChecking=no", fmt.Sprintf("%s@%s", c.user, c.host)}
	} else {
		args = []string{"-i", c.keyPath, "-p", fmt.Sprintf("%d", c.port), "-o", "StrictHostKeyChecking=no", fmt.Sprintf("%s@%s", c.user, c.host)}
	}

	cmd := exec.Command(sshCmd, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// Tunnel opens a native Go SSH port-forward: localhost:localPort → remoteHost:remotePort.
// Uses the already-established SSH connection so it works on all platforms without WSL.
// The underlying SSH connection is self-healing: if it drops (e.g. a network blip),
// it is transparently reconnected without tearing down the local listener.
// Blocks until the user presses Ctrl+C.
func (c *Client) Tunnel(localPort int, remoteHost string, remotePort int) error {
	listenAddr := fmt.Sprintf("0.0.0.0:%d", localPort)
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %v", listenAddr, err)
	}
	defer listener.Close()

	stop := make(chan struct{})
	defer close(stop)
	go c.healLoop(stop)

	remoteAddr := fmt.Sprintf("%s:%d", remoteHost, remotePort)

	for {
		localConn, err := listener.Accept()
		if err != nil {
			return err
		}

		go c.forwardTunnelConn(localConn, remoteAddr)
	}
}

// dialChannel opens a forwarded connection, bounded so a dead-but-open SSH
// connection cannot park the caller forever waiting for a channel that will
// never be confirmed.
func dialChannel(client *ssh.Client, remoteAddr string) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := client.Dial("tcp", remoteAddr)
		ch <- result{conn, err}
	}()

	select {
	case r := <-ch:
		return r.conn, r.err
	case <-time.After(channelTimeout):
		go func() {
			if r := <-ch; r.conn != nil {
				r.conn.Close()
			}
		}()
		return nil, fmt.Errorf("timed out opening channel to %s after %s", remoteAddr, channelTimeout)
	}
}

// dialRemote dials remoteAddr over the SSH connection, healing it and retrying
// once if the attempt fails. It does not probe liveness up front: that cost a
// full round trip on every forwarded connection, and healLoop already probes
// in the background.
func (c *Client) dialRemote(remoteAddr string) (net.Conn, error) {
	client := c.sshClient()
	if client != nil {
		conn, err := dialChannel(client, remoteAddr)
		if err == nil {
			return conn, nil
		}
	}

	if err := c.reconnectFrom(client); err != nil {
		return nil, fmt.Errorf("ssh connection down, reconnect failed: %v", err)
	}
	fmt.Fprintln(os.Stderr, "✅ Reconnected.")

	client = c.sshClient()
	if client == nil {
		return nil, fmt.Errorf("ssh connection unavailable")
	}
	return dialChannel(client, remoteAddr)
}

func (c *Client) forwardTunnelConn(localConn net.Conn, remoteAddr string) {
	remoteConn, err := c.dialRemote(remoteAddr)
	if err != nil {
		localConn.Close()
		fmt.Fprintf(os.Stderr, "Remote connection failed: %v\n", err)
		return
	}

	defer localConn.Close()
	defer remoteConn.Close()
	go io.Copy(remoteConn, localConn)
	io.Copy(localConn, remoteConn)
}

func (c *Client) RunInteractiveCommand(cmd string) error {
	client := c.sshClient()
	if client == nil {
		return fmt.Errorf("ssh connection is not established")
	}
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}

	fd := int(os.Stdin.Fd())
	width, height, err := term.GetSize(fd)
	if err != nil {
		width, height = 80, 40
	}

	if err := session.RequestPty("xterm-256color", height, width, modes); err != nil {
		return fmt.Errorf("request for pseudo terminal failed: %v", err)
	}

	session.Stdin = os.Stdin
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %v", err)
	}
	defer term.Restore(fd, oldState)

	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("failed to start command: %v", err)
	}

	return session.Wait()
}

func (c *Client) SimulatedSession() error {
	client := c.sshClient()
	if client == nil {
		return fmt.Errorf("ssh connection is not established")
	}
	session, err := client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()

	// Set up terminal modes
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,     // enable echoing
		ssh.TTY_OP_ISPEED: 14400, // input speed = 14.4kbaud
		ssh.TTY_OP_OSPEED: 14400, // output speed = 14.4kbaud
	}

	// Get terminal size
	fd := int(os.Stdin.Fd())
	width, height, err := term.GetSize(fd)
	if err != nil {
		width, height = 80, 40 // Fallback
	}

	// Request pseudo terminal
	if err := session.RequestPty("xterm-256color", height, width, modes); err != nil {
		return fmt.Errorf("request for pseudo terminal failed: %v", err)
	}

	session.Stdin = os.Stdin
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	// Put local terminal into raw mode
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %v", err)
	}
	defer term.Restore(fd, oldState)

	// Start shell on remote
	if err := session.Shell(); err != nil {
		return fmt.Errorf("failed to start shell: %v", err)
	}

	// Wait for session to finish
	return session.Wait()
}

func (c *Client) UploadFile(local, remote string) error {
	src, err := os.Open(local)
	if err != nil {
		return err
	}
	defer src.Close()

	sftpClient, err := c.sftpClient()
	if err != nil {
		return err
	}

	dst, err := sftpClient.Create(remote)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

func (c *Client) DownloadFile(remote, local string) error {
	sftpClient, err := c.sftpClient()
	if err != nil {
		return err
	}

	src, err := sftpClient.Open(remote)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(local)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

// RsyncDirectory syncs a local directory to a remote directory using rsync over SSH
// This is much faster than creating tarballs as it only transfers changed files
func (c *Client) RsyncDirectory(localDir, remoteDir string, stdout, stderr io.Writer) error {
	// Find rsync executable
	rsyncCmd, err := findRsync()
	if err != nil {
		return err
	}

	// Essential hardcoded exclusions (always excluded regardless of .gitignore)
	essentialExcludes := []string{
		//".git",
		"node_modules",
		".next",

		"*.log",
	}

	// Build base args
	args := []string{
		"-avz",
		"--delete",
	}

	// Add essential exclusions
	for _, pattern := range essentialExcludes {
		args = append(args, "--exclude="+pattern)
	}

	// Try to read .gitignore from the local directory
	gitignorePath := filepath.Join(localDir, ".gitignore")
	gitignorePatterns := parseGitignore(gitignorePath)

	// Add gitignore patterns as exclusions
	for _, pattern := range gitignorePatterns {
		args = append(args, "--exclude="+pattern)
	}

	// Prepare paths based on rsync type
	sshKeyPath := c.keyPath
	localPath := localDir

	// For Git Bash, Cygwin, and WSL, convert Windows paths to Unix format
	if rsyncCmd != "rsync" {
		useWSLFormat := (rsyncCmd == "wsl")

		if useWSLFormat {
			// For WSL, copy SSH key to WSL filesystem to fix permissions issue
			// Windows filesystem doesn't support Unix permissions properly
			wslKeyPath := "~/.ssh/graft_key.pem"

			// Convert Windows path to WSL path for copying
			windowsKeyWSL := convertToUnixPath(c.keyPath, true)

			// Copy key to WSL filesystem and set proper permissions
			copyCmd := exec.Command("wsl", "bash", "-c",
				fmt.Sprintf("mkdir -p ~/.ssh && cp '%s' %s && chmod 600 %s",
					windowsKeyWSL, wslKeyPath, wslKeyPath))
			if err := copyCmd.Run(); err != nil {
				return fmt.Errorf("failed to copy SSH key to WSL: %v", err)
			}

			sshKeyPath = wslKeyPath
			localPath = convertToUnixPath(localDir, true)
		} else {
			sshKeyPath = convertToUnixPath(c.keyPath, false)
			localPath = convertToUnixPath(localDir, false)
		}
	}

	// Add SSH configuration and paths
	// Quote the SSH key path to handle spaces and special characters
	args = append(args,
		"-e",
		fmt.Sprintf("ssh -i \"%s\" -p %d -o StrictHostKeyChecking=no", sshKeyPath, c.port),
		localPath+"/",
		fmt.Sprintf("%s@%s:%s/", c.user, c.host, remoteDir),
	)

	// Execute rsync
	var cmd *exec.Cmd
	if rsyncCmd == "wsl" {
		// For WSL, prepend rsync command
		wslArgs := append([]string{"rsync"}, args...)
		cmd = exec.Command("wsl", wslArgs...)
	} else {
		cmd = exec.Command(rsyncCmd, args...)
	}

	cmd.Stdout = stdout
	cmd.Stderr = stderr

	return cmd.Run()
}

// PullRsync syncs a remote directory to a local directory using rsync over SSH
func (c *Client) PullRsync(remoteDir, localDir string, stdout, stderr io.Writer) error {
	// Find rsync executable
	rsyncCmd, err := findRsync()
	if err != nil {
		return err
	}

	// Build base args
	args := []string{
		"-avz",
	}

	// Prepare paths based on rsync type
	sshKeyPath := c.keyPath
	localPath := localDir

	// For Git Bash, Cygwin, and WSL, convert Windows paths to Unix format
	if rsyncCmd != "rsync" {
		useWSLFormat := (rsyncCmd == "wsl")

		if useWSLFormat {
			wslKeyPath := "~/.ssh/graft_key.pem"
			windowsKeyWSL := convertToUnixPath(c.keyPath, true)

			copyCmd := exec.Command("wsl", "bash", "-c",
				fmt.Sprintf("mkdir -p ~/.ssh && cp '%s' %s && chmod 600 %s",
					windowsKeyWSL, wslKeyPath, wslKeyPath))
			if err := copyCmd.Run(); err != nil {
				return fmt.Errorf("failed to copy SSH key to WSL: %v", err)
			}

			sshKeyPath = wslKeyPath
			localPath = convertToUnixPath(localDir, true)
		} else {
			sshKeyPath = convertToUnixPath(c.keyPath, false)
			localPath = convertToUnixPath(localDir, false)
		}
	}

	// Add SSH configuration and paths
	args = append(args,
		"-e",
		fmt.Sprintf("ssh -i \"%s\" -p %d -o StrictHostKeyChecking=no", sshKeyPath, c.port),
		fmt.Sprintf("%s@%s:%s/", c.user, c.host, remoteDir),
		localPath+"/",
	)

	// Execute rsync
	var cmd *exec.Cmd
	if rsyncCmd == "wsl" {
		wslArgs := append([]string{"rsync"}, args...)
		cmd = exec.Command("wsl", wslArgs...)
	} else {
		cmd = exec.Command(rsyncCmd, args...)
	}

	cmd.Stdout = stdout
	cmd.Stderr = stderr

	return cmd.Run()
}

func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.sftp != nil {
		c.sftp.Close()
	}
	if c.client != nil {
		c.client.Close()
	}
}
