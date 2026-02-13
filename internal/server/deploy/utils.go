package deploy

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DockerComposeFile represents the structure we need from docker-compose.yml
type DockerComposeFile struct {
	Version  string                    `yaml:"version"`
	Services map[string]ComposeService `yaml:"services"`
	Networks map[string]interface{}    `yaml:"networks,omitempty"`
	Volumes  map[string]interface{}    `yaml:"volumes,omitempty"`
}

type ComposeService struct {
	Build       *BuildConfig           `yaml:"build,omitempty"`
	Image       string                 `yaml:"image,omitempty"`
	Environment interface{}            `yaml:"environment,omitempty"`
	EnvFiles    interface{}            `yaml:"env_file,omitempty"`
	Labels      []string               `yaml:"labels,omitempty"`
	OtherFields map[string]interface{} `yaml:",inline"`
}

func (s *ComposeService) GetEnvFiles() []string {
	if s.EnvFiles == nil {
		return nil
	}
	switch v := s.EnvFiles.(type) {
	case string:
		return []string{v}
	case []interface{}:
		var result []string
		for _, item := range v {
			if str, ok := item.(string); ok {
				result = append(result, str)
			}
		}
		return result
	case []string:
		return v
	}
	return nil
}

type BuildConfig struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile,omitempty"`
}

func ParseComposeFile(path string, domain string) (*DockerComposeFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Replace domain placeholder if provided
	if domain != "" {
		data = []byte(strings.ReplaceAll(string(data), "${GRAFT_DOMAIN}", domain))
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
func ProcessServiceEnvironment(serviceName string, service *ComposeService, secrets map[string]string, envname string) ([]string, error) {
	var finalEnvLines []string
	actualEnvFiles := service.GetEnvFiles()

	// 1. First, process literal environment variables from the compose file
	switch env := service.Environment.(type) {
	case map[string]interface{}:
		for k, v := range env {
			val := fmt.Sprintf("%v", v)
			finalEnvLines = append(finalEnvLines, fmt.Sprintf("%s=%s", k, val))
		}
	case []interface{}:
		for _, v := range env {
			finalEnvLines = append(finalEnvLines, fmt.Sprintf("%v", v))
		}
	case []string:
		finalEnvLines = append(finalEnvLines, env...)
	}

	// 2. Next, process matched env_files
	for _, envPath := range actualEnvFiles {
		basename := filepath.Base(envPath)
		
		// Logic: 
		// - Explicit matches (e.g. .env.prod for prod, .env.dev for dev) are always included.
		// - Generic matches (.env, env, backend.env - files with 0 or 1 dots) 
		//   are ONLY included for the "prod" environment (user convention: generic = prod).
		
		isExplicitlyOurs := strings.Contains(basename, "."+envname) || 
							strings.Contains(basename, envname+".") ||
							strings.HasSuffix(basename, "."+envname)
		
		// A file is considered "generic/base" if it has only one dot (like backend.env or .env)
		isGeneric := strings.Count(basename, ".") <= 1

		shouldInclude := false
		if isExplicitlyOurs {
			shouldInclude = true
		} else if isGeneric {
			// Include generic/base files ONLY for production environment
			if envname == "prod" {
				shouldInclude = true
			}
		}

		if shouldInclude {
			content, err := os.ReadFile(envPath)
			if err == nil {
				fmt.Printf("   🔗 Merging env file: %s\n", envPath)
				// Clean content and add to lines
				cStr := strings.TrimSpace(string(content))
				if cStr != "" {
					finalEnvLines = append(finalEnvLines, cStr)
				}
			} else {
				fmt.Printf("   ⚠️  Skip/Error reading env file %s: %v\n", envPath, err)
			}
		} else {
			fmt.Printf("   ⏭️  Skipping env file: %s (not scoped for %s)\n", envPath, envname)
		}
	}

	// If we found nothing at all, don't modify the service or write files
	if len(finalEnvLines) == 0 {
		return nil, nil
	}

	// 3. Resolve secrets in the collected environment lines
	for i := range finalEnvLines {
		for key, value := range secrets {
			finalEnvLines[i] = strings.ReplaceAll(finalEnvLines[i], fmt.Sprintf("${%s}", key), value)
		}
	}

	// 4. Create local env directory and save the merged file
	if err := os.MkdirAll("env", 0755); err != nil {
		return nil, err
	}

	// File naming: service.env.prod
	mergedFileName := fmt.Sprintf("%s.env.%s", serviceName, envname)
	mergedEnvPath := filepath.Join("env", mergedFileName)
	
	err := os.WriteFile(mergedEnvPath, []byte(strings.Join(finalEnvLines, "\n")), 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to write merged env file %s: %v", mergedEnvPath, err)
	}

	// 5. Update the service struct for the universal docker-compose.yml
	service.Environment = nil
	// Universal path on server: env/service.env
	service.EnvFiles = []string{"./env/" + serviceName + ".env"}

	return []string{mergedEnvPath}, nil
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
