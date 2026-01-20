package deploy

import (
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

// SyncService syncs only a specific service
func SyncService(envname string, client *ssh.Client, p *Project, serviceName string, noCache, heave, useGit bool, gitBranch, gitCommit string, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "🎯 Syncing service: %s\n", serviceName)

	// Perform backup before sync if configured
	if err := PerformBackup(client, p, stdout, stderr); err != nil {
		fmt.Fprintf(stdout, "⚠️  Backup warning: %v\n", err)
	}

	remoteProjName := p.Name
	if !strings.HasSuffix(remoteProjName, "-"+envname) {
		remoteProjName = fmt.Sprintf("%s-%s", remoteProjName, envname)
	}
	remoteDir := fmt.Sprintf("/opt/graft/projects/%s", remoteProjName)

	// Update project metadata with current remote path
	meta := &config.ProjectMetadata{
		Name:       p.Name,
		RemotePath: remoteDir,
	}
	if err := config.SaveProjectMetadata(envname, meta); err != nil {
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
				// Fallback to metadata branch if available
				meta, _ := config.LoadProjectMetadata(envname)
				if meta != nil && meta.GitBranch != "" {
					branch = meta.GitBranch
				} else {
					branch, err = git.GetCurrentBranch(".")
					if err != nil {
						return fmt.Errorf("failed to get current branch: %v", err)
					}
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

func Sync(envname string, client *ssh.Client, p *Project, noCache, heave, useGit bool, gitBranch, gitCommit string, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "🚀 Syncing project: %s\n", p.Name)

	// Perform backup before sync if configured
	if err := PerformBackup(client, p, stdout, stderr); err != nil {
		fmt.Fprintf(stdout, "⚠️  Backup warning: %v\n", err)
	}

	remoteProjName := p.Name
	if !strings.HasSuffix(remoteProjName, "-"+envname) {
		remoteProjName = fmt.Sprintf("%s-%s", remoteProjName, envname)
	}
	remoteDir := fmt.Sprintf("/opt/graft/projects/%s", remoteProjName)

	// Update project metadata with current remote path
	meta := &config.ProjectMetadata{
		Name:       p.Name,
		RemotePath: remoteDir,
	}
	if err := config.SaveProjectMetadata(envname, meta); err != nil {
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
			// Fallback to metadata branch if available
			meta, _ := config.LoadProjectMetadata(envname)
			if meta != nil && meta.GitBranch != "" {
				branch = meta.GitBranch
			} else {
				branch, err = git.GetCurrentBranch(".")
				if err != nil {
					return fmt.Errorf("failed to get current branch: %v", err)
				}
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

// SyncComposeOnly uploads only the docker-compose.yml and restarts services
func SyncComposeOnly(envname string, client *ssh.Client, p *Project, heave bool, stdout, stderr io.Writer, doCompose bool, doEnv bool) error {
	if !doCompose && !doEnv {
		return fmt.Errorf("at least one of doCompose or doEnv must be true")
	}
	printstr := ""
	if doCompose {
		printstr += "compose"
	}
	if doEnv {
		if printstr != "" {
			printstr += " and "
		}
		printstr += "env"
	}
	fmt.Fprintf(stdout, "📄 Syncing %s file only...\n", printstr)

	remoteProjName := p.Name
	if !strings.HasSuffix(remoteProjName, "-"+envname) {
		remoteProjName = fmt.Sprintf("%s-%s", remoteProjName, envname)
	}
	remoteDir := fmt.Sprintf("/opt/graft/projects/%s", remoteProjName)

	// Perform backup before sync if configured

	if !strings.HasPrefix(p.DeploymentMode, "git") {
		if err := PerformBackup(client, p, stdout, stderr); err != nil {
			fmt.Fprintf(stdout, "⚠️  Backup warning: %v\n", err)
		}
	}

	// Ensure remote projects directory exists and is owned by the user
	// We do this once at the beginning to handle both compose and env sync cases
	// Use -R to ensure existing files (like docker-compose.yml) are also owned by the user
	if err := client.RunCommand(fmt.Sprintf("sudo mkdir -p %s && sudo chown -R $USER:$USER %s", remoteDir, remoteDir), stdout, stderr); err != nil {
		return fmt.Errorf("failed to prepare remote directory: %v", err)
	}

	if doCompose {
		// Find and parse the local graft-compose.yml file
		localFile := "graft-compose.yml"
		compose, err := ParseComposeFile(localFile)
		if err != nil {
			return fmt.Errorf("failed to parse compose file: %v", err)
		}
		if _, err := os.Stat(localFile); err != nil {
			return fmt.Errorf("project file not found: %s", localFile)
		}

		// Load secrets
		secrets, _ := config.LoadSecrets()

		// Process environments and handle git-images mode transformation
		for sName := range compose.Services {

			sPtr := compose.Services[sName]
			ProcessServiceEnvironment(sName, &sPtr, secrets)

			// If in git-images mode and has build, replace with GHCR image
			mode := getGraftMode(sPtr.Labels)
			if mode == "git-images" && sPtr.Build != nil {
				remoteURL, err := git.GetRemoteURL(".", "origin")
				if err == nil {
					ownerRepo := ""
					if strings.HasPrefix(remoteURL, "https://") {
						parts := strings.Split(strings.TrimSuffix(remoteURL, ".git"), "/")
						if len(parts) >= 2 {
							ownerRepo = parts[len(parts)-2] + "/" + parts[len(parts)-1]
						}
					} else if strings.HasPrefix(remoteURL, "git@") {
						parts := strings.Split(strings.TrimSuffix(remoteURL, ".git"), ":")
						if len(parts) >= 2 {
							ownerRepo = parts[1]
						}
					}

					if ownerRepo != "" {
						sPtr.Image = fmt.Sprintf("ghcr.io/%s/%s:latest", strings.ToLower(ownerRepo), sName)
						sPtr.Build = nil // Remove build context
					}
				}
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
	}

	// Upload env directory if it exists
	if doEnv {
		localFile := "docker-compose.yml"
		compose, err := ParseComposeFile(localFile)
		if err != nil {
			return fmt.Errorf("failed to parse compose file: %v", err)
		}
		fmt.Fprintf(stdout, "📤 Uploading environment files...\n")

		for service := range compose.Services {
			envpaths := compose.Services[service].EnvFiles
			if len(envpaths) > 0 {
				for _, envPath := range envpaths {
					// Construct remote path
					remoteEnvPath := path.Join(remoteDir, envPath)

					// Extract directory from the env file path
					remoteEnvDir := path.Dir(remoteEnvPath)

					// Create parent directory on remote server
					if err := client.RunCommand(fmt.Sprintf("mkdir -p %s", remoteEnvDir), stdout, stderr); err != nil {
						return fmt.Errorf("failed to create remote directory %s: %v", remoteEnvDir, err)
					}

					// Upload the env file
					if err := client.UploadFile(envPath, remoteEnvPath); err != nil {
						return fmt.Errorf("failed to upload environment file %s for service %s: %v", envPath, service, err)
					}
				}
			}
		}
	}
	if doCompose {
		// Upload the generated docker-compose.yml
		remoteCompose := path.Join(remoteDir, "docker-compose.yml")
		fmt.Fprintf(stdout, "🔍 Verifying local %s exists...\n", "docker-compose.yml")
		if _, err := os.Stat("docker-compose.yml"); err != nil {
			return fmt.Errorf("local docker-compose.yml was not generated: %v", err)
		}

		fmt.Fprintf(stdout, "📤 Uploading generated docker-compose.yml to %s...\n", remoteCompose)
		if err := client.UploadFile("docker-compose.yml", remoteCompose); err != nil {
			return fmt.Errorf("failed to upload docker-compose.yml: %v", err)
		}

	}

	fmt.Fprintf(stdout, "✅ %s file uploaded!\n", printstr)

	if !heave {
		// Restart services without rebuilding
		fmt.Fprintln(stdout, "🔄 Restarting services...")
		if err := client.RunCommand(fmt.Sprintf("cd %s && sudo docker compose up -d --remove-orphans", remoteDir), stdout, stderr); err != nil {
			return err
		}
	}

	fmt.Fprintf(stdout, "✅ %s file synced!\n", printstr)

	return nil
}
