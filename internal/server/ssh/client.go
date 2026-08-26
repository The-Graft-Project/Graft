package ssh

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

type Client struct {
	client  *ssh.Client
	sftp    *sftp.Client
	host    string
	port    int
	user    string
	keyPath string

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

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("unable to start sftp: %v", err)
	}

	c.client = client
	c.sftp = sftpClient

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

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("unable to get home directory: %v", err)
	}

	knownHostsPath := filepath.Join(homeDir, ".ssh", "known_hosts")
	hostKeyCallback := createHostKeyCallback(knownHostsPath, c.host)

	config := &ssh.ClientConfig{
		User: c.user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", c.host, c.port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("unable to connect: %v", err)
	}
	return client, nil
}

// sshClient returns the currently active SSH connection.
func (c *Client) sshClient() *ssh.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client
}

// isAlive checks whether the active SSH connection is still usable.
func (c *Client) isAlive() bool {
	client := c.sshClient()
	if client == nil {
		return false
	}
	_, _, err := client.SendRequest("graft-keepalive@graft", true, nil)
	return err == nil
}

// reconnect re-establishes the SSH connection, replacing the active one.
func (c *Client) reconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	newClient, err := c.dial()
	if err != nil {
		return err
	}
	if c.client != nil {
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
	session, err := c.client.NewSession()
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
	session, err := c.client.NewSession()
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

// dialRemote dials remoteAddr over the SSH connection, healing it first if it
// has died and retrying once more after a fresh reconnect.
func (c *Client) dialRemote(remoteAddr string) (net.Conn, error) {
	if !c.isAlive() {
		if err := c.reconnect(); err != nil {
			return nil, fmt.Errorf("ssh connection down, reconnect failed: %v", err)
		}
		fmt.Fprintln(os.Stderr, "✅ Reconnected.")
	}

	conn, err := c.sshClient().Dial("tcp", remoteAddr)
	if err == nil {
		return conn, nil
	}

	if rerr := c.reconnect(); rerr != nil {
		return nil, err
	}
	fmt.Fprintln(os.Stderr, "✅ Reconnected.")
	return c.sshClient().Dial("tcp", remoteAddr)
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
	session, err := c.client.NewSession()
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
	session, err := c.client.NewSession()
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

	dst, err := c.sftp.Create(remote)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	return err
}

func (c *Client) DownloadFile(remote, local string) error {
	src, err := c.sftp.Open(remote)
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
