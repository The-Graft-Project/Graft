package ssh

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
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
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read private key: %v", err)
	}

	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("unable to parse private key: %v", err)
	}

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
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
		keyPath: keyPath,
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

// parseGitignore reads a .gitignore file and returns rsync-compatible exclude patterns
func parseGitignore(gitignorePath string) []string {
	var excludes []string
	
	file, err := os.Open(gitignorePath)
	if err != nil {
		// If .gitignore doesn't exist, return empty list
		return excludes
	}
	defer file.Close()
	
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		
		// Remove leading slash for rsync compatibility
		pattern := strings.TrimPrefix(line, "/")
		
		// Skip negation patterns (rsync handles them differently)
		if strings.HasPrefix(pattern, "!") {
			continue
		}
		
		excludes = append(excludes, pattern)
	}
	
	return excludes
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

// findRsync tries to find rsync executable, checking common Windows locations
func findRsync() (string, error) {
	// On Windows, check specific locations first to properly identify the rsync type
	windowsPaths := []string{
		"C:\\Program Files\\Git\\usr\\bin\\rsync.exe", // Git Bash
		"C:\\cygwin64\\bin\\rsync.exe",                // Cygwin 64-bit
		"C:\\cygwin\\bin\\rsync.exe",                  // Cygwin 32-bit
	}
	
	for _, path := range windowsPaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	
	// Check if WSL is available
	if _, err := exec.LookPath("wsl"); err == nil {
		// Verify WSL has rsync
		cmd := exec.Command("wsl", "which", "rsync")
		if err := cmd.Run(); err == nil {
			return "wsl", nil
		}
	}
	
	// Try standard rsync (Linux/Mac or rsync in PATH)
	if _, err := exec.LookPath("rsync"); err == nil {
		return "rsync", nil
	}
	
	return "", fmt.Errorf("rsync not found - please install rsync via WSL, Git for Windows, or Cygwin")
}

// convertToUnixPath converts Windows paths to Unix-style paths
// For WSL: C:\Users\Name\file.pem -> /mnt/c/Users/Name/file.pem
// For Git Bash/Cygwin: C:\Users\Name\file.pem -> /c/Users/Name/file.pem
func convertToUnixPath(windowsPath string, useWSLFormat bool) string {
	// Clean the path first to remove any redundant separators
	cleanPath := filepath.Clean(windowsPath)
	
	// Replace backslashes with forward slashes
	unixPath := filepath.ToSlash(cleanPath)
	
	// Convert drive letter
	if len(unixPath) >= 2 && unixPath[1] == ':' {
		drive := strings.ToLower(string(unixPath[0]))
		if useWSLFormat {
			// WSL format: /mnt/c/Users/...
			unixPath = "/mnt/" + drive + unixPath[2:]
		} else {
			// Git Bash/Cygwin format: /c/Users/...
			unixPath = "/" + drive + unixPath[2:]
		}
	}
	
	return unixPath
}

func (c *Client) Close() {
	if c.sftp != nil {
		c.sftp.Close()
	}
	if c.client != nil {
		c.client.Close()
	}
}
