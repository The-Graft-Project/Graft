package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const RemoteInfraPath = "/opt/graft/infra/.config"
const RemoteProjectsPath = "/opt/graft/config/projects.json"

type ServerConfig struct {
	RegistryName string `json:"registry_name,omitempty"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	User         string `json:"user"`
	KeyPath      string `json:"key_path"`
	GraftHookURL string `json:"graft_hook_url,omitempty"`
}

type InfraConfig struct {
	PostgresUser     string    `json:"postgres_user"`
	PostgresPassword string    `json:"postgres_password"`
	PostgresDB       string    `json:"postgres_db"`
	PostgresPort     string    `json:"postgres_port,omitempty"`
	RedisPort        string    `json:"redis_port,omitempty"`
	S3               *S3Config `json:"s3,omitempty"`
}

type S3Config struct {
	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

type CloudflareConfig struct {
	APIToken string `json:"api_token,omitempty"`
	ZoneID   string `json:"zone_id,omitempty"`
	Domain   string `json:"domain,omitempty"`
}

type Cloudflare struct {
	Cloudflare         CloudflareConfig            `json:"cloudflare,omitempty"`
	CloudflareAccounts map[string]CloudflareConfig `json:"cloudflare_accounts,omitempty"`
}

type GlobalConfig struct {
	Servers  map[string]ServerConfig `json:"servers"`
	Projects map[string]string       `json:"projects"`
}

func GetGlobalRegistryPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".graft", "registry.json")
}

func GetGlobalConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".graft", "config.json")
}

func LoadCloudFlareConfig() (*Cloudflare, error) {

	// Try global if local fails or if local is missing Cloudflare
	globalPath := GetGlobalConfigPath()
	gCfg, gErr := loadFile(globalPath)

	return gCfg, gErr

}

func loadFile(path string) (*Cloudflare, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Cloudflare
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func LoadGlobalConfig() (*GlobalConfig, error) {
	path := GetGlobalRegistryPath()
	data, err := os.ReadFile(path)
	if err != nil {
		// Return empty registry if not found
		return &GlobalConfig{
			Servers:  make(map[string]ServerConfig),
			Projects: make(map[string]string),
		}, nil
	}

	var cfg GlobalConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Servers == nil {
		cfg.Servers = make(map[string]ServerConfig)
	}
	if cfg.Projects == nil {
		cfg.Projects = make(map[string]string)
	}

	return &cfg, nil
}

func SaveGlobalConfig(cfg *GlobalConfig) error {
	path := GetGlobalRegistryPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

func SaveGlobalCloudflare(apiToken, zoneID, domain string) error {
	globalPath := GetGlobalConfigPath()
	cfg, err := loadFile(globalPath)
	if err != nil {
		cfg = &Cloudflare{}
	}

	if cfg.CloudflareAccounts == nil {
		cfg.CloudflareAccounts = make(map[string]CloudflareConfig)
	}

	cfg.CloudflareAccounts[domain] = CloudflareConfig{
		APIToken: apiToken,
		ZoneID:   zoneID,
		Domain:   domain,
	}

	return nil
}

func SaveSecret(key, value string) error {
	dir := ".graft"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	path := filepath.Join(dir, "secrets.env")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(fmt.Sprintf("%s=%s\n", key, value))
	return err
}

func LoadSecrets() (map[string]string, error) {
	path := filepath.Join(".graft", "secrets.env")
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, err
	}
	defer file.Close()

	secrets := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			secrets[parts[0]] = parts[1]
		}
	}

	return secrets, scanner.Err()
}

// ProjectMetadata stores local project information
type ProjectMetadata struct {
	Name            string `json:"name"`
	RemotePath      string `json:"remote_path"`
	Initialized     bool   `json:"initialized"`
	DeploymentMode  string `json:"deployment_mode,omitempty"` // "git-images", "git-repo-serverbuild", "git-manual", "direct-serverbuild", "direct-localbuild"
	GitBranch       string `json:"git_branch,omitempty"`
	GraftHookURL    string `json:"graft_hook_url,omitempty"`
	RollbackBackups int    `json:"rollback_backups,omitempty"`
	Registry        string `json:"env,omitempty"`
}
type ProjectEnv struct {
	Name            string `json:"name"`
	DeploymentMode  string `json:"deployment_mode,omitempty"` // "git-images", "git-repo-serverbuild", "git-manual", "direct-serverbuild", "direct-localbuild"
	RollbackBackups int    `json:"rollback_backups,omitempty"`

	Env map[string]*ProjectMetadata
}

// SaveProjectMetadata saves project metadata to .graft/project.json
func SaveProjectMetadata(envname string, meta *ProjectMetadata) error {
	dir := ".graft"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	path := filepath.Join(dir, "project.json")

	// 1. Load existing data or initialize new
	projectData := ProjectEnv{
		Env: make(map[string]*ProjectMetadata),
	}

	// Try to read existing file to avoid overwriting other environments
	if fileData, err := os.ReadFile(path); err == nil {
		json.Unmarshal(fileData, &projectData)
	}
	if projectData.Name == "" {
		projectData.Name = meta.Name
		projectData.DeploymentMode = meta.DeploymentMode
		projectData.RollbackBackups = meta.RollbackBackups
	} else {
		meta.Name = projectData.Name
		meta.DeploymentMode = projectData.DeploymentMode
		meta.RollbackBackups = projectData.RollbackBackups
	}
	// 2. Update the specific environment
	if !strings.HasSuffix(meta.Name, "-"+envname) {
		meta.Name = meta.Name + "-" + envname
	}
	projectData.Env[envname] = meta

	// 3. Marshal the entire map (so you don't lose other environments)
	data, err := json.MarshalIndent(projectData, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}

	// 4. Register globally
	absPath, _ := filepath.Abs(".")
	gCfg, _ := LoadGlobalConfig()
	if gCfg != nil {
		if gCfg.Projects == nil {
			gCfg.Projects = make(map[string]string)
		}
		gCfg.Projects[projectData.Name] = absPath
		SaveGlobalConfig(gCfg)
	}

	return nil
}

// LoadProjectMetadata loads project metadata from .graft/project.json
func LoadProjectMetadata(name string) (*ProjectMetadata, error) {
	path := filepath.Join(".graft", "project.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var ProjectEnv ProjectEnv
	if err := json.Unmarshal(data, &ProjectEnv); err != nil {
		return nil, err
	}
	meta := ProjectEnv.Env[name]
	return meta, nil
}
