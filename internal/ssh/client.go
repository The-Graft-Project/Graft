package ssh

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

	key, err := os.ReadFile(actualKeyPath)
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
	hostKeyCallback := createHostKeyCallback(knownHostsPath, host)

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("unable to connect: %v", err)
	}

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("unable to start sftp: %v", err)
	}

	return &Client{
		client:  client,
		sftp:    sftpClient,
		host:    host,
		port:    port,
		user:    user,
		keyPath: actualKeyPath,
	}, nil
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

	// If on Windows and no WSL, use simulated session as fallback
	// This avoids the strict file permission requirements of Windows OpenSSH
	if runtime.GOOS == "windows" && !isWSL {
		fmt.Println("⚠️  WSL not detected. Using simulated terminal (fallback). Please install WSL to get a better experience.")
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
	if c.sftp != nil {
		c.sftp.Close()
	}
	if c.client != nil {
		c.client.Close()
	}
}
