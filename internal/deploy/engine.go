package deploy

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
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
	Networks map[string]interface{}       `yaml:"networks,omitempty"`
	Volumes  map[string]interface{}       `yaml:"volumes,omitempty"`
}

type ComposeService struct {
	Build       *BuildConfig           `yaml:"build,omitempty"`
	Image       string                 `yaml:"image,omitempty"`
	Environment interface{}            `yaml:"environment,omitempty"`
	EnvFiles    []string               `yaml:"env_file,omitempty"`
	Labels      []string               `yaml:"labels,omitempty"`
	OtherFields map[string]interface{} `yaml:",inline"`
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

// ParseComposeFile parses docker-compose.yml to extract service configurations
func ParseComposeFile(path string) (*DockerComposeFile, error) {
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

// ProcessServiceEnvironment extracts environment variables, resolves secrets, and writes to an .env file
func ProcessServiceEnvironment(serviceName string, service *ComposeService, secrets map[string]string) ([]string, error) {
	var envLines []string

	// Handle environment as interface{} (could be map or slice)
	switch env := service.Environment.(type) {
	case map[string]interface{}:
		for k, v := range env {
			val := fmt.Sprintf("%v", v)
			envLines = append(envLines, fmt.Sprintf("%s=%s", k, val))
		}
	case []interface{}:
		for _, v := range env {
			envLines = append(envLines, fmt.Sprintf("%v", v))
		}
	}

	if len(envLines) == 0 {
		return nil, nil
	}

	// Resolve secrets in environment variables
	for i := range envLines {
		for key, value := range secrets {
			envLines[i] = strings.ReplaceAll(envLines[i], fmt.Sprintf("${%s}", key), value)
		}
	}

	// Create env directory
	if err := os.MkdirAll("env", 0755); err != nil {
		return nil, err
	}

	// Write to env/service.env
	envFileRelPath := filepath.Join("env", serviceName+".env")
	err := os.WriteFile(envFileRelPath, []byte(strings.Join(envLines, "\n")), 0644)
	if err != nil {
		return nil, err
	}

	// Update service to use env_file and clear environment
	service.Environment = nil
	
	// Keep existing env_files if any
	service.EnvFiles = append(service.EnvFiles, "./"+filepath.ToSlash(envFileRelPath))

	return []string{envFileRelPath}, nil
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

	// Perform backup before sync if configured
	if err := PerformBackup(client, p, stdout, stderr); err != nil {
		fmt.Fprintf(stdout, "⚠️  Backup warning: %v\n", err)
	}

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
	compose, err := ParseComposeFile(localFile)
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

	// Check if this is an image-based service (no build context)
	isImageBased := service.Image != "" && service.Build == nil
	
	// Load secrets
	secrets, _ := config.LoadSecrets()

	// Process environments for ALL services to ensure consistency in the generated docker-compose.yml
	for sName := range compose.Services {
		// Use a pointer to update the service in the map
		sPtr := compose.Services[sName]
		ProcessServiceEnvironment(sName, &sPtr, secrets)
		compose.Services[sName] = sPtr
	}

	// Generate the actual docker-compose.yml content
	updatedComposeData, err := yaml.Marshal(compose)
	if err != nil {
		return fmt.Errorf("failed to marshal updated compose file: %v", err)
	}

	// Save the actual docker-compose.yml locally
	if err := os.WriteFile("docker-compose.yml", updatedComposeData, 0644); err != nil {
		return fmt.Errorf("failed to save docker-compose.yml: %v", err)
	}

	// Ensure .gitignore is up to date
	EnsureGitignore(".")

	// Ensure remote projects directory exists
	if err := client.RunCommand(fmt.Sprintf("sudo mkdir -p %s && sudo chown $USER:$USER %s", remoteDir, remoteDir), stdout, stderr); err != nil {
		return err
	}

	// Upload env directory if it exists
	if _, err := os.Stat("env"); err == nil {
		fmt.Fprintf(stdout, "📤 Uploading environment files...\n")
		remoteEnvDir := path.Join(remoteDir, "env")
		client.RunCommand(fmt.Sprintf("mkdir -p %s", remoteEnvDir), stdout, stderr)
		
		// Map local env/* to remote env/*
		files, _ := os.ReadDir("env")
		for _, f := range files {
			if !f.IsDir() {
				localEnvPath := filepath.Join("env", f.Name())
				remoteEnvPath := path.Join(remoteEnvDir, f.Name())
				client.UploadFile(localEnvPath, remoteEnvPath)
			}
		}
	}

	// Upload the generated docker-compose.yml
	remoteCompose := path.Join(remoteDir, "docker-compose.yml")
	fmt.Fprintf(stdout, "📤 Uploading generated docker-compose.yml...\n")
	if err := client.UploadFile("docker-compose.yml", remoteCompose); err != nil {
		return err
	}

	if isImageBased {
		// For image-based services, handle pull and restart
		fmt.Fprintf(stdout, "🖼️  Image-based service detected: %s\n", service.Image)
		
		if heave {
			return nil // Heave sync ends here
		}

		// Stop the old container
		fmt.Fprintf(stdout, "🛑 Stopping old container...\n")
		stopCmd := fmt.Sprintf("cd %s && sudo docker compose stop %s && sudo docker compose rm -f %s", remoteDir, serviceName, serviceName)
		client.RunCommand(stopCmd, stdout, stderr) // Ignore errors if container doesn't exist

		// Pull the latest image
		fmt.Fprintf(stdout, "📥 Pulling latest image...\n")
		pullCmd := fmt.Sprintf("cd %s && sudo docker compose pull %s", remoteDir, serviceName)
		if err := client.RunCommand(pullCmd, stdout, stderr); err != nil {
			return fmt.Errorf("image pull failed: %v", err)
		}

		// Start the service with the new image
		fmt.Fprintf(stdout, "🚀 Starting %s...\n", serviceName)
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

	// Perform backup before sync if configured
	if err := PerformBackup(client, p, stdout, stderr); err != nil {
		fmt.Fprintf(stdout, "⚠️  Backup warning: %v\n", err)
	}

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
	compose, err := ParseComposeFile(localFile)
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

	// Load secrets
	secrets, _ := config.LoadSecrets()

	// Process environments for ALL services
	for sName := range compose.Services {
		sPtr := compose.Services[sName]
		ProcessServiceEnvironment(sName, &sPtr, secrets)
		
		// For serverbuild services, update build context to point to uploaded code
		mode := getGraftMode(sPtr.Labels)
		if mode == "serverbuild" && sPtr.Build != nil {
			contextName := filepath.Base(sPtr.Build.Context)
			if contextName == "." || contextName == "/" {
				contextName = sName
			}
			sPtr.Build.Context = "./" + contextName
		}
		compose.Services[sName] = sPtr
	}

	// Generate the actual docker-compose.yml content
	updatedComposeData, err := yaml.Marshal(compose)
	if err != nil {
		return fmt.Errorf("failed to marshal updated compose file: %v", err)
	}

	// Save the actual docker-compose.yml locally
	if err := os.WriteFile("docker-compose.yml", updatedComposeData, 0644); err != nil {
		return fmt.Errorf("failed to save docker-compose.yml: %v", err)
	}

	// Ensure .gitignore is up to date
	EnsureGitignore(".")

	// Upload env directory if it exists
	if _, err := os.Stat("env"); err == nil {
		fmt.Fprintf(stdout, "\n📤 Uploading environment files...\n")
		remoteEnvDir := path.Join(remoteDir, "env")
		client.RunCommand(fmt.Sprintf("mkdir -p %s", remoteEnvDir), stdout, stderr)
		
		files, _ := os.ReadDir("env")
		for _, f := range files {
			if !f.IsDir() {
				localEnvPath := filepath.Join("env", f.Name())
				remoteEnvPath := path.Join(remoteEnvDir, f.Name())
				client.UploadFile(localEnvPath, remoteEnvPath)
			}
		}
	}

	// Upload docker-compose.yml
	remoteCompose := path.Join(remoteDir, "docker-compose.yml")
	fmt.Fprintln(stdout, "\n📤 Uploading generated docker-compose.yml...")
	if err := client.UploadFile("docker-compose.yml", remoteCompose); err != nil {
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

	fmt.Fprintln(stdout, "🚀 Starting services...")
	if err := client.RunCommand(fmt.Sprintf("cd %s && sudo docker compose up -d --pull always --remove-orphans", remoteDir), stdout, stderr); err != nil {
		return err
	}

	// Cleanup: Remove only dangling images
	fmt.Fprintln(stdout, "🧹 Cleaning up old images...")
	cleanupCmd := "sudo docker image prune -f"
	client.RunCommand(cleanupCmd, stdout, stderr)

	fmt.Fprintln(stdout, "✅ Deployment complete!")
	return nil
}

// ExtractTraefikHosts extracts all Host() rules from Traefik labels
func ExtractTraefikHosts(labels []string) []string {
	var hosts []string
	
	for _, label := range labels {
		// Look for traefik.http.routers.*.rule labels
		if strings.Contains(label, "traefik.http.routers.") && strings.Contains(label, ".rule=") {
			// Extract the value after .rule=
			parts := strings.SplitN(label, ".rule=", 2)
			if len(parts) != 2 {
				continue
			}
			
			rule := parts[1]
			
			// Extract Host(`domain.com`) patterns
			// Support both Host(`...`) and Host("...")
			for {
				startIdx := strings.Index(rule, "Host(")
				if startIdx == -1 {
					break
				}
				
				// Find the matching closing parenthesis
				rule = rule[startIdx+5:] // Skip "Host("
				
				var host string
				if strings.HasPrefix(rule, "`") {
					// Backtick format: Host(`domain.com`)
					endIdx := strings.Index(rule[1:], "`")
					if endIdx == -1 {
						break
					}
					host = rule[1 : endIdx+1]
					rule = rule[endIdx+2:]
				} else if strings.HasPrefix(rule, "\"") {
					// Quote format: Host("domain.com")
					endIdx := strings.Index(rule[1:], "\"")
					if endIdx == -1 {
						break
					}
					host = rule[1 : endIdx+1]
					rule = rule[endIdx+2:]
				} else {
					break
				}
				
				if host != "" {
					hosts = append(hosts, host)
				}
			}
		}
	}
	
	return hosts
}

func PerformBackup(client *ssh.Client, p *Project, stdout, stderr io.Writer) error {
	if p.RollbackBackups <= 0 {
		return nil
	}

	remoteDir := fmt.Sprintf("/opt/graft/projects/%s", p.Name)

	// Check if project exists on remote
	if err := client.RunCommand(fmt.Sprintf("ls %s/docker-compose.yml", remoteDir), nil, nil); err != nil {
		return nil
	}

	// 1. Get timestamp
	out, err := client.GetCommandOutput("date +%Y%m%d%H%M%S")
	if err != nil {
		return fmt.Errorf("failed to get timestamp: %v", err)
	}
	timestamp := strings.TrimSpace(out)

	backupBase := fmt.Sprintf("/opt/graft/backup/%s", p.Name)
	backupDir := fmt.Sprintf("%s/%s", backupBase, timestamp)

	fmt.Fprintf(stdout, "\n📦 Creating rollback backup: %s\n", timestamp)

	// Create backup dirs
	client.RunCommand(fmt.Sprintf("sudo mkdir -p %s/compose %s/images", backupDir, backupDir), stdout, stderr)

	// Backup docker-compose.yml and env/
	client.RunCommand(fmt.Sprintf("sudo cp %s/docker-compose.yml %s/compose/ 2>/dev/null", remoteDir, backupDir), stdout, stderr)
	client.RunCommand(fmt.Sprintf("sudo cp -r %s/env %s/compose/ 2>/dev/null", remoteDir, backupDir), stdout, stderr)

	// Backup images
	fmt.Fprintf(stdout, "  🖼️  Saving service images...\n")
	saveImagesCmd := fmt.Sprintf("cd %s && for img in $(sudo docker compose images -q); do "+
		"name=$(sudo docker image inspect $img --format '{{index .RepoTags 0}}' | tr ':/' '__'); "+
		"[ -n \"$name\" ] && [ \"$name\" != \"<nil>\" ] && sudo docker save $img -o %s/images/\"$name\".tar; done", remoteDir, backupDir)
	client.RunCommand(saveImagesCmd, stdout, stderr)

	// Clean up old backups
	cleanCmd := fmt.Sprintf("cd %s && ls -1dt * | tail -n +%d | xargs -I {} sudo rm -rf {}", backupBase, p.RollbackBackups+1)
	client.RunCommand(cleanCmd, stdout, stderr)

	return nil
}

func RestoreRollback(client *ssh.Client, p *Project, backupTimestamp string, stdout, stderr io.Writer) error {
	remoteDir := fmt.Sprintf("/opt/graft/projects/%s", p.Name)
	backupDir := fmt.Sprintf("/opt/graft/backup/%s/%s", p.Name, backupTimestamp)

	fmt.Fprintf(stdout, "⏪ Rolling back to version %s...\n", backupTimestamp)

	// 1. Restore files
	client.RunCommand(fmt.Sprintf("sudo cp %s/compose/docker-compose.yml %s/", backupDir, remoteDir), stdout, stderr)
	client.RunCommand(fmt.Sprintf("sudo cp -r %s/compose/env %s/ 2>/dev/null", backupDir, remoteDir), stdout, stderr)

	// 2. Load images if any
	fmt.Fprintf(stdout, "📥 Loading backed up images...\n")
	loadCmd := fmt.Sprintf("for img in %s/images/*.tar; do [ -f \"$img\" ] && sudo docker load -i \"$img\"; done", backupDir)
	client.RunCommand(loadCmd, stdout, stderr)

	// 3. Restart services
	fmt.Fprintf(stdout, "🚀 Restarting services...\n")
	restartCmd := fmt.Sprintf("cd %s && sudo docker compose up -d --remove-orphans", remoteDir)
	return client.RunCommand(restartCmd, stdout, stderr)
}
