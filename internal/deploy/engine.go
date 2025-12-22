package deploy

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/git"
	"github.com/skssmd/graft/internal/ssh"
	"gopkg.in/yaml.v3"
)

// DockerComposeFile represents the structure we need from docker-compose.yml
type DockerComposeFile struct {
	Version  string                       `yaml:"version"`
	Services map[string]ComposeService    `yaml:"services"`
	Networks map[string]interface{}       `yaml:"networks"`
}

type ComposeService struct {
	Build       *BuildConfig          `yaml:"build,omitempty"`
	Image       string                `yaml:"image,omitempty"`
	Environment []string              `yaml:"environment,omitempty"`
	Labels      []string              `yaml:"labels,omitempty"`
	Networks    []string              `yaml:"networks,omitempty"`
	Restart     string                `yaml:"restart,omitempty"`
}

type BuildConfig struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile,omitempty"`
}

func LoadProject(path string) (*Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Project
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, err
	}

	// Fallback for projects where Name/Domain aren't in YAML
	if p.Name == "" {
		meta, err := config.LoadProjectMetadata()
		if err == nil {
			p.Name = meta.Name
		}
	}

	return &p, nil
}

// Parse docker-compose.yml to extract service configurations
func parseComposeFile(path string) (*DockerComposeFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var compose DockerComposeFile
	if err := yaml.Unmarshal(data, &compose); err != nil {
		return nil, err
	}
	return &compose, nil
}

// Extract graft.mode from service labels
func getGraftMode(labels []string) string {
	for _, label := range labels {
		if strings.HasPrefix(label, "graft.mode=") {
			return strings.TrimPrefix(label, "graft.mode=")
		}
	}
	return "localbuild" // default
}

// Create a tarball of a directory
func createTarball(sourceDir, tarballPath string) error {
	file, err := os.Create(tarballPath)
	if err != nil {
		return err
	}
	defer file.Close()

	gzipWriter := gzip.NewWriter(file)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip .git, node_modules, etc.
		if info.IsDir() && (info.Name() == ".git" || info.Name() == "node_modules" || info.Name() == ".next") {
			return filepath.SkipDir
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		// Convert Windows backslashes to Unix forward slashes for tar archive
		header.Name = filepath.ToSlash(relPath)

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		if !info.IsDir() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			_, err = io.Copy(tarWriter, file)
			return err
		}

		return nil
	})
}

// SyncService syncs only a specific service
func SyncService(client *ssh.Client, p *Project, serviceName string, noCache, heave, useGit bool, gitBranch, gitCommit string, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "🎯 Syncing service: %s\n", serviceName)

	remoteDir := fmt.Sprintf("/opt/graft/projects/%s", p.Name)
	
	// Update project metadata with current remote path
	meta := &config.ProjectMetadata{
		Name:       p.Name,
		RemotePath: remoteDir,
	}
	if err := config.SaveProjectMetadata(meta); err != nil {
		fmt.Fprintf(stdout, "Warning: Could not save project metadata: %v\n", err)
	}
	
	// Find and parse the local graft.yml file
	localFile := "graft-compose.yml"
	if _, err := os.Stat(localFile); err != nil {
		return fmt.Errorf("project file not found: %s", localFile)
	}

	// Parse compose file to get service configuration
	compose, err := parseComposeFile(localFile)
	if err != nil {
		return fmt.Errorf("failed to parse compose file: %v", err)
	}

	// Check if service exists
	service, exists := compose.Services[serviceName]
	if !exists {
		return fmt.Errorf("service '%s' not found in compose file", serviceName)
	}

	mode := getGraftMode(service.Labels)
	fmt.Fprintf(stdout, "📦 Mode: %s\n", mode)

	if mode == "serverbuild" && service.Build != nil {
		// Upload source code for this service only
		contextPath := service.Build.Context
		if !filepath.IsAbs(contextPath) {
			contextPath = filepath.Clean(contextPath)
		}

		// Verify build context exists
		if _, err := os.Stat(contextPath); os.IsNotExist(err) {
			return fmt.Errorf("build context directory not found: %s\n👉 Please ensure the directory exists or update 'context' in your graft.yml file.", contextPath)
		}

		// Verify Dockerfile exists within context
		dockerfileName := service.Build.Dockerfile
		if dockerfileName == "" {
			dockerfileName = "Dockerfile"
		}
		dockerfilePath := filepath.Join(contextPath, dockerfileName)
		if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
			return fmt.Errorf("Dockerfile not found: %s\n👉 Checked path: %s\n👉 Please check the 'dockerfile' field in your graft.yml and ensure the file exists and casing matches EXACTLY (Linux is case-sensitive!).", dockerfileName, dockerfilePath)
		}

		// Handle git-based sync if enabled
		var actualContextPath string
		var cleanupFunc func()
		
		if useGit {
			// Check if git repo exists
			if !git.HasGitRepo(".") {
				return fmt.Errorf("--git flag used but no git repository found (.git directory missing)")
			}
			
			// Determine branch
			branch := gitBranch
			if branch == "" {
				branch, err = git.GetCurrentBranch(".")
				if err != nil {
					return fmt.Errorf("failed to get current branch: %v", err)
				}
			}
			
			// Determine commit
			commit := gitCommit
			if commit == "" {
				commit, err = git.GetLatestCommit(".", branch)
				if err != nil {
					return fmt.Errorf("failed to get latest commit: %v", err)
				}
			}
			
			fmt.Fprintf(stdout, "📦 Git mode: branch=%s, commit=%s\n", branch, commit[:7])
			
			// Create temp directory for git export
			tempDir, err := os.MkdirTemp("", "graft-git-*")
			if err != nil {
				return fmt.Errorf("failed to create temp directory: %v", err)
			}
			
			// Setup cleanup
			cleanupFunc = func() {
				os.RemoveAll(tempDir)
			}
			defer cleanupFunc()
			
			// Export git commit to tarball (filter to service context path)
			tarballPath := filepath.Join(tempDir, "export.tar.gz")
			contextRelPath := contextPath
			if filepath.IsAbs(contextPath) {
				contextRelPath, _ = filepath.Rel(".", contextPath)
			}
			
			err = git.CreateArchive(".", commit, tarballPath, []string{contextRelPath + "/"})
			if err != nil {
				return fmt.Errorf("failed to create git archive: %v", err)
			}
			
			// Extract to temp directory
			extractDir := filepath.Join(tempDir, "extracted")
			err = git.ExtractArchive(tarballPath, extractDir)
			if err != nil {
				return fmt.Errorf("failed to extract git archive: %v", err)
			}
			
			// Update context path to extracted directory
			actualContextPath = filepath.Join(extractDir, contextRelPath)
			fmt.Fprintf(stdout, "📦 Exported git commit to temp directory\n")
		} else {
			actualContextPath = contextPath
		}

		fmt.Fprintf(stdout, "📦 Syncing source code with rsync (incremental)...\n")
		contextName := filepath.Base(contextPath)
		if contextName == "." || contextName == "/" {
			contextName = serviceName
		}

		// Use rsync to sync the directory
		serviceDir := path.Join(remoteDir, contextName)
		
		// Ensure remote directory exists
		if err := client.RunCommand(fmt.Sprintf("mkdir -p %s", serviceDir), stdout, stderr); err != nil {
			return fmt.Errorf("failed to create remote directory: %v", err)
		}
		
		// Try rsync first, fall back to tarball if rsync is not available
		fmt.Fprintf(stdout, "📤 Uploading changes from %s...\n", actualContextPath)
		rsyncErr := client.RsyncDirectory(actualContextPath, serviceDir, stdout, stderr)
		
		if rsyncErr != nil {
			// Check if error is due to rsync not being found
			if strings.Contains(rsyncErr.Error(), "rsync not found") {
				fmt.Fprintf(stdout, "⚠️  Rsync not available, falling back to tarball method...\n")
				
				// Fall back to tarball method
				tarballPath := filepath.Join(os.TempDir(), fmt.Sprintf("%s-%s.tar.gz", p.Name, contextName))
				if err := createTarball(actualContextPath, tarballPath); err != nil {
					return fmt.Errorf("failed to create tarball: %v", err)
				}
				defer os.Remove(tarballPath)

				// Upload tarball to server
				remoteTarball := path.Join(remoteDir, fmt.Sprintf("%s.tar.gz", contextName))
				fmt.Fprintf(stdout, "📤 Uploading tarball...\n")
				if err := client.UploadFile(tarballPath, remoteTarball); err != nil {
					return fmt.Errorf("failed to upload tarball: %v", err)
				}

				// Extract on server
				extractCmd := fmt.Sprintf("rm -rf %s && mkdir -p %s && tar -xzf %s -C %s && rm %s", 
					serviceDir, serviceDir, remoteTarball, serviceDir, remoteTarball)
				fmt.Fprintf(stdout, "📂 Extracting on server...\n")
				if err := client.RunCommand(extractCmd, stdout, stderr); err != nil {
					return fmt.Errorf("failed to extract tarball: %v", err)
				}
			} else {
				return fmt.Errorf("failed to sync directory: %v", rsyncErr)
			}
		}

		// Inject secrets and update context in compose file
		content, err := os.ReadFile(localFile)
		if err != nil {
			return err
		}

		secrets, _ := config.LoadSecrets()
		contentStr := string(content)
		for key, value := range secrets {
			contentStr = strings.ReplaceAll(contentStr, fmt.Sprintf("${%s}", key), value)
		}

		// Update context for this service
		reContext := regexp.MustCompile(fmt.Sprintf(`(?m)(^\s+)context:\s*%s\b`, regexp.QuoteMeta(service.Build.Context)))
		contentStr = reContext.ReplaceAllString(contentStr, fmt.Sprintf(`${1}context: ./%s`, contextName))
		
		reBuild := regexp.MustCompile(fmt.Sprintf(`(?m)(^\s+)build:\s*%s\b`, regexp.QuoteMeta(service.Build.Context)))
		contentStr = reBuild.ReplaceAllString(contentStr, fmt.Sprintf(`${1}build: ./%s`, contextName))

		// Upload modified docker-compose.yml
		tmpFile := filepath.Join(os.TempDir(), "docker-compose.yml")
		if err := os.WriteFile(tmpFile, []byte(contentStr), 0644); err != nil {
			return err
		}
		defer os.Remove(tmpFile)

		remoteCompose := path.Join(remoteDir, "docker-compose.yml")
		if err := client.UploadFile(tmpFile, remoteCompose); err != nil {
			return err
		}
		if heave {
			return nil // Heave sync ends here
		}
	}

	// Stop and remove the old container
	fmt.Fprintf(stdout, "🛑 Stopping old container...\n")
	stopCmd := fmt.Sprintf("cd %s && sudo docker compose stop %s && sudo docker compose rm -f %s", remoteDir, serviceName, serviceName)
	client.RunCommand(stopCmd, stdout, stderr) // Ignore errors if container doesn't exist

	// Conditionally clear build cache
	if noCache {
		fmt.Fprintf(stdout, "🧹 Clearing build cache for fresh build...\n")
		pruneCmd := "sudo docker builder prune -f"
		client.RunCommand(pruneCmd, stdout, stderr) // Ignore errors
	}
	
	// Build the service (separate command to show build logs)
	fmt.Fprintf(stdout, "🔨 Building %s...\n", serviceName)
	var buildCmd string
	if noCache {
		buildCmd = fmt.Sprintf("cd %s && sudo docker compose build --no-cache %s", remoteDir, serviceName)
	} else {
		buildCmd = fmt.Sprintf("cd %s && sudo docker compose build %s", remoteDir, serviceName)
	}
	
	if err := client.RunCommand(buildCmd, stdout, stderr); err != nil {
		return fmt.Errorf("build failed: %v", err)
	}
	
	// Start the service
	fmt.Fprintf(stdout, "� Starting %s...\n", serviceName)
	upCmd := fmt.Sprintf("cd %s && sudo docker compose up -d %s", remoteDir, serviceName)
	if err := client.RunCommand(upCmd, stdout, stderr); err != nil {
		return err
	}

	// Cleanup old images
	fmt.Fprintln(stdout, "🧹 Cleaning up old images...")
	cleanupCmd := "sudo docker image prune -f"
	if err := client.RunCommand(cleanupCmd, stdout, stderr); err != nil {
		fmt.Fprintf(stdout, "⚠️  Cleanup warning: %v\n", err)
	}

	return nil
}

func Sync(client *ssh.Client, p *Project, noCache, heave, useGit bool, gitBranch, gitCommit string, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "🚀 Syncing project: %s\n", p.Name)

	remoteDir := fmt.Sprintf("/opt/graft/projects/%s", p.Name)
	
	// Update project metadata with current remote path
	meta := &config.ProjectMetadata{
		Name:       p.Name,
		RemotePath: remoteDir,
	}
	if err := config.SaveProjectMetadata(meta); err != nil {
		fmt.Fprintf(stdout, "Warning: Could not save project metadata: %v\n", err)
	}
	
	if err := client.RunCommand(fmt.Sprintf("sudo mkdir -p %s && sudo chown $USER:$USER %s", remoteDir, remoteDir), stdout, stderr); err != nil {
		return err
	}

	// Find and parse the local graft.yml file
	localFile := "graft-compose.yml"
	if _, err := os.Stat(localFile); err != nil {
		return fmt.Errorf("project file not found: %s", localFile)
	}

	// Parse compose file to get service configurations
	compose, err := parseComposeFile(localFile)
	if err != nil {
		return fmt.Errorf("failed to parse compose file: %v", err)
	}

	// Handle git-based sync if enabled
	var workingDir string
	var cleanupFunc func()
	
	if useGit {
		// Check if git repo exists
		if !git.HasGitRepo(".") {
			return fmt.Errorf("--git flag used but no git repository found (.git directory missing)")
		}
		
		// Determine branch
		branch := gitBranch
		if branch == "" {
			branch, err = git.GetCurrentBranch(".")
			if err != nil {
				return fmt.Errorf("failed to get current branch: %v", err)
			}
		}
		
		// Determine commit
		commit := gitCommit
		if commit == "" {
			commit, err = git.GetLatestCommit(".", branch)
			if err != nil {
				return fmt.Errorf("failed to get latest commit: %v", err)
			}
		}
		
		fmt.Fprintf(stdout, "📦 Git mode: branch=%s, commit=%s\n", branch, commit[:7])
		
		// Create temp directory for git export
		tempDir, err := os.MkdirTemp("", "graft-git-*")
		if err != nil {
			return fmt.Errorf("failed to create temp directory: %v", err)
		}
		
		// Setup cleanup
		cleanupFunc = func() {
			os.RemoveAll(tempDir)
		}
		defer cleanupFunc()
		
		// Export entire git commit to tarball
		tarballPath := filepath.Join(tempDir, "export.tar.gz")
		err = git.CreateArchive(".", commit, tarballPath, nil) // nil = export everything
		if err != nil {
			return fmt.Errorf("failed to create git archive: %v", err)
		}
		
		// Extract to temp directory
		extractDir := filepath.Join(tempDir, "extracted")
		err = git.ExtractArchive(tarballPath, extractDir)
		if err != nil {
			return fmt.Errorf("failed to extract git archive: %v", err)
		}
		
		workingDir = extractDir
		fmt.Fprintf(stdout, "📦 Exported git commit to temp directory\n")
	} else {
		workingDir = "."
	}

	// Process each service based on graft.mode
	for serviceName, service := range compose.Services {
		mode := getGraftMode(service.Labels)
		fmt.Fprintf(stdout, "\n📦 Processing service '%s' (mode: %s)\n", serviceName, mode)

		if mode == "serverbuild" && service.Build != nil {
			// Upload source code and build on server
			contextPath := service.Build.Context
			if !filepath.IsAbs(contextPath) {
				contextPath = filepath.Clean(contextPath)
			}
			
			// If using git, resolve context path relative to workingDir
			if useGit {
				contextPath = filepath.Join(workingDir, contextPath)
			}

			// Verify build context exists
			if _, err := os.Stat(contextPath); os.IsNotExist(err) {
				return fmt.Errorf("build context directory not found: %s\n👉 Please ensure the directory exists or update 'context' in your graft.yml file.", contextPath)
			}

			// Verify Dockerfile exists within context
			dockerfileName := service.Build.Dockerfile
			if dockerfileName == "" {
				dockerfileName = "Dockerfile"
			}
			dockerfilePath := filepath.Join(contextPath, dockerfileName)
			if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
				return fmt.Errorf("Dockerfile not found: %s\n👉 Checked path: %s\n👉 Please check the 'dockerfile' field in your graft.yml and ensure the file exists and casing matches EXACTLY (Linux is case-sensitive!).", dockerfileName, dockerfilePath)
			}

			fmt.Fprintf(stdout, "  📦 Syncing source code with rsync (incremental)...\n")
			contextName := filepath.Base(contextPath)
			if contextName == "." || contextName == "/" {
				contextName = serviceName
			}

			// Use rsync to sync the directory
			serviceDir := path.Join(remoteDir, contextName)
			
			// Ensure remote directory exists
			if err := client.RunCommand(fmt.Sprintf("mkdir -p %s", serviceDir), stdout, stderr); err != nil {
				return fmt.Errorf("failed to create remote directory: %v", err)
			}
			
			// Try rsync first, fall back to tarball if rsync is not available
			fmt.Fprintf(stdout, "  📤 Uploading changes from %s...\n", contextPath)
			rsyncErr := client.RsyncDirectory(contextPath, serviceDir, stdout, stderr)
			
			if rsyncErr != nil {
				// Check if error is due to rsync not being found
				if strings.Contains(rsyncErr.Error(), "rsync not found") {
					fmt.Fprintf(stdout, "  ⚠️  Rsync not available, falling back to tarball method...\n")
					
					// Fall back to tarball method
					tarballPath := filepath.Join(os.TempDir(), fmt.Sprintf("%s-%s.tar.gz", p.Name, contextName))
					if err := createTarball(contextPath, tarballPath); err != nil {
						return fmt.Errorf("failed to create tarball: %v", err)
					}
					defer os.Remove(tarballPath)

					// Upload tarball to server
					remoteTarball := path.Join(remoteDir, fmt.Sprintf("%s.tar.gz", contextName))
					fmt.Fprintf(stdout, "  📤 Uploading tarball...\n")
					if err := client.UploadFile(tarballPath, remoteTarball); err != nil {
						return fmt.Errorf("failed to upload tarball: %v", err)
					}

					// Extract on server
					extractCmd := fmt.Sprintf("mkdir -p %s && tar -xzf %s -C %s && rm %s", 
						serviceDir, remoteTarball, serviceDir, remoteTarball)
					fmt.Fprintf(stdout, "  📂 Extracting on server...\n")
					if err := client.RunCommand(extractCmd, stdout, stderr); err != nil {
						return fmt.Errorf("failed to extract tarball: %v", err)
					}
				} else {
					return fmt.Errorf("failed to sync directory: %v", rsyncErr)
				}
			}
		}
	}

	// Inject secrets into the compose file
	content, err := os.ReadFile(localFile)
	if err != nil {
		return err
	}

	secrets, _ := config.LoadSecrets()
	contentStr := string(content)
	for key, value := range secrets {
		contentStr = strings.ReplaceAll(contentStr, fmt.Sprintf("${%s}", key), value)
	}

	// For serverbuild services, update build context to point to uploaded code
	for serviceName, service := range compose.Services {
		mode := getGraftMode(service.Labels)
		if mode == "serverbuild" && service.Build != nil {
			contextName := filepath.Base(service.Build.Context)
			if contextName == "." || contextName == "/" {
				contextName = serviceName
			}
			
			// Update context to point to the extracted directory
			reContext := regexp.MustCompile(fmt.Sprintf(`(?m)(^\s+)context:\s*%s\b`, regexp.QuoteMeta(service.Build.Context)))
			contentStr = reContext.ReplaceAllString(contentStr, fmt.Sprintf(`${1}context: ./%s`, contextName))
			
			reBuild := regexp.MustCompile(fmt.Sprintf(`(?m)(^\s+)build:\s*%s\b`, regexp.QuoteMeta(service.Build.Context)))
			contentStr = reBuild.ReplaceAllString(contentStr, fmt.Sprintf(`${1}build: ./%s`, contextName))
		}
	}

	// Write modified compose file
	tmpFile := filepath.Join(os.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(tmpFile, []byte(contentStr), 0644); err != nil {
		return err
	}
	defer os.Remove(tmpFile)

	// Upload docker-compose.yml
	remoteCompose := path.Join(remoteDir, "docker-compose.yml")
	fmt.Fprintln(stdout, "\n📤 Uploading docker-compose.yml...")
	if err := client.UploadFile(tmpFile, remoteCompose); err != nil {
		return err
	}

	if heave {
		fmt.Fprintln(stdout, "✅ Heave sync complete (upload only)!")
		return nil
	}

	// Build and start services
	if noCache {
		fmt.Fprintln(stdout, "🧹 Clearing build cache for fresh build...")
		pruneCmd := "sudo docker builder prune -f"
		client.RunCommand(pruneCmd, stdout, stderr) // Ignore errors
		
		fmt.Fprintln(stdout, "🔨 Building services (no cache)...")
		if err := client.RunCommand(fmt.Sprintf("cd %s && sudo docker compose build --no-cache", remoteDir), stdout, stderr); err != nil {
			return fmt.Errorf("build failed: %v", err)
		}
	} else {
		fmt.Fprintln(stdout, "🔨 Building services...")
		if err := client.RunCommand(fmt.Sprintf("cd %s && sudo docker compose build", remoteDir), stdout, stderr); err != nil {
			return fmt.Errorf("build failed: %v", err)
		}
	}

	fmt.Fprintln(stdout, "� Starting services...")
	if err := client.RunCommand(fmt.Sprintf("cd %s && sudo docker compose up -d --remove-orphans", remoteDir), stdout, stderr); err != nil {
		return err
	}

	// Cleanup: Remove only dangling images (keep build cache for faster rebuilds)
	fmt.Fprintln(stdout, "🧹 Cleaning up old images...")
	cleanupCmd := "sudo docker image prune -f"
	if err := client.RunCommand(cleanupCmd, stdout, stderr); err != nil {
		// Don't fail deployment if cleanup fails, just warn
		fmt.Fprintf(stdout, "⚠️  Cleanup warning: %v\n", err)
	}

	fmt.Fprintln(stdout, "✅ Deployment complete!")
	return nil
}
