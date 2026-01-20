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
	EnvFiles    []string               `yaml:"env_file,omitempty"`
	Labels      []string               `yaml:"labels,omitempty"`
	OtherFields map[string]interface{} `yaml:",inline"`
}

type BuildConfig struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile,omitempty"`
}

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
