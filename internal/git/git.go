package git

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HasGitRepo checks if the directory contains a .git folder
func HasGitRepo(dir string) bool {
	gitDir := filepath.Join(dir, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// GetCurrentBranch returns the current git branch name
func GetCurrentBranch(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get current branch: %v", err)
	}
	
	return strings.TrimSpace(string(output)), nil
}

// GetLatestCommit returns the latest commit hash on the specified branch
func GetLatestCommit(dir, branch string) (string, error) {
	cmd := exec.Command("git", "rev-parse", branch)
	cmd.Dir = dir
	
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get latest commit for branch %s: %v", branch, err)
	}
	
	return strings.TrimSpace(string(output)), nil
}

// CreateArchive exports a git commit to a tarball, optionally filtering to specific paths
// paths: optional list of paths to include (e.g., ["frontend/"] for service filtering)
func CreateArchive(dir, commit, outputPath string, paths []string) error {
	// Build git archive command
	args := []string{"archive", "--format=tar.gz", "-o", outputPath, commit}
	
	// Add path filters if specified
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create git archive: %v\nOutput: %s", err, string(output))
	}
	
	return nil
}

// ExtractArchive extracts a tarball to the specified directory
func ExtractArchive(tarballPath, destDir string) error {
	// Create destination directory
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %v", err)
	}
	
	// Open tarball file
	file, err := os.Open(tarballPath)
	if err != nil {
		return fmt.Errorf("failed to open tarball: %v", err)
	}
	defer file.Close()
	
	// Create gzip reader
	gzr, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %v", err)
	}
	defer gzr.Close()
	
	// Create tar reader
	tr := tar.NewReader(gzr)
	
	// Extract files
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %v", err)
		}
		
		// Construct target path
		target := filepath.Join(destDir, header.Name)
		
		// Ensure target is within destDir (security check)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)) {
			return fmt.Errorf("illegal file path in archive: %s", header.Name)
		}
		
		switch header.Typeflag {
		case tar.TypeDir:
			// Create directory
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %v", target, err)
			}
		case tar.TypeReg:
			// Create parent directory
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %v", err)
			}
			
			// Create file
			outFile, err := os.Create(target)
			if err != nil {
				return fmt.Errorf("failed to create file %s: %v", target, err)
			}
			
			// Copy content
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return fmt.Errorf("failed to extract file %s: %v", target, err)
			}
			outFile.Close()
		}
	}
	
	return nil
}
