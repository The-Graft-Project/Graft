package ssh

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func createHostKeyCallback(knownHostsPath, hostname string) ssh.HostKeyCallback {
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		// Ensure .ssh directory exists
		sshDir := filepath.Dir(knownHostsPath)
		if err := os.MkdirAll(sshDir, 0700); err != nil {
			return fmt.Errorf("could not create .ssh directory: %v", err)
		}

		// Try to load existing known_hosts
		kh, err := knownhosts.New(knownHostsPath)
		if err == nil {
			// known_hosts exists, check if host is known
			err := kh(host, remote, key)
			if err == nil {
				// Host key matches
				return nil
			}

			// Check if it's just "host key not found" vs "host key mismatch"
			var keyErr *knownhosts.KeyError
			if errors.As(err, &keyErr) && len(keyErr.Want) > 0 {
				// Host exists but key doesn't match - SECURITY WARNING
				return fmt.Errorf("WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!\nHost key for %s has changed. This could indicate a man-in-the-middle attack.\nTo fix: ssh-keygen -R %s", hostname, hostname)
			}
			// Host not found, will add below
		}

		// Host not in known_hosts - add it
		f, err := os.OpenFile(knownHostsPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
		if err != nil {
			return fmt.Errorf("could not open known_hosts: %v", err)
		}
		defer f.Close()

		// Write the host key
		line := knownhosts.Line([]string{hostname}, key)
		if _, err := fmt.Fprintf(f, "%s\n", line); err != nil {
			return fmt.Errorf("could not write to known_hosts: %v", err)
		}

		fmt.Printf("✓ Added %s to known_hosts\n", hostname)
		return nil
	}
}

// findSSH attempts to find the best SSH client. On Windows, it prefers WSL to avoid permission issues.
func findSSH() (string, bool) {
	// Only check for WSL on Windows
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("wsl"); err == nil {
			cmd := exec.Command("wsl", "which", "ssh")
			if err := cmd.Run(); err == nil {
				return "wsl", true
			}
		}
	}

	// Try standard ssh
	if path, err := exec.LookPath("ssh"); err == nil {
		return path, false
	}

	return "ssh", false
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
