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
}

type InfraConfig struct {
	PostgresUser     string `json:"postgres_user"`
	PostgresPassword string `json:"postgres_password"`
	PostgresDB       string `json:"postgres_db"`
}

type GraftConfig struct {
	Server ServerConfig `json:"server"`
	Infra  InfraConfig  `json:"infra,omitempty"`
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

func GetLocalConfigPath() string {
	return filepath.Join(".graft", "config.json")
}

func LoadConfig() (*GraftConfig, error) {
	// Try local first
	localPath := GetLocalConfigPath()
	cfg, err := loadFile(localPath)
	if err == nil {
		return cfg, nil
	}

	// Try global
	globalPath := GetGlobalConfigPath()
	return loadFile(globalPath)
}

func loadFile(path string) (*GraftConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg GraftConfig
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
	if cfg.Servers == nil { cfg.Servers = make(map[string]ServerConfig) }
	if cfg.Projects == nil { cfg.Projects = make(map[string]string) }

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

func SaveConfig(cfg *GraftConfig, local bool) error {
	path := GetGlobalConfigPath()
	if local {
		path = GetLocalConfigPath()
	}

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
	Name       string `json:"name"`
	RemotePath string `json:"remote_path"`
	Initialized bool `json:"initialized"`
}

// SaveProjectMetadata saves project metadata to .graft/project.json and registers it globally
func SaveProjectMetadata(meta *ProjectMetadata) error {
	dir := ".graft"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	path := filepath.Join(dir, "project.json")
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}

	// Register globally
	absPath, _ := filepath.Abs(".")
	gCfg, _ := LoadGlobalConfig()
	if gCfg != nil {
		if gCfg.Projects == nil { gCfg.Projects = make(map[string]string) }
		gCfg.Projects[meta.Name] = absPath
		SaveGlobalConfig(gCfg)
	}

	return nil
}

// LoadProjectMetadata loads project metadata from .graft/project.json
func LoadProjectMetadata() (*ProjectMetadata, error) {
	path := filepath.Join(".graft", "project.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var meta ProjectMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}

	return &meta, nil
}
