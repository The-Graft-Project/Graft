package executors

import (
	"fmt"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/server/ssh"
)

type Executor struct {
	Env          string
	Server       *config.ServerConfig

	Client       *ssh.Client
	GlobalConfig *config.GlobalConfig
	ProjectMeta  *config.ProjectMetadata
	
}


func GetExecutor() *Executor {
	globalConfig, _ := config.LoadGlobalConfig()
	return &Executor{
		Env:          "prod",
		GlobalConfig: globalConfig,
	}
}
func(e *Executor) getProjectMeta() (*config.ProjectMetadata, error) {
	if e.ProjectMeta != nil {
		return e.ProjectMeta, nil
	}
	meta, err := config.LoadProjectMetadata(e.Env)
	if err != nil {
		fmt.Println("Error loading project metadata.")
		return nil, err
	}
	e.ProjectMeta = meta
	return meta, nil
}
func(e *Executor) saveProjectMeta(meta *config.ProjectMetadata) error {
	if meta == nil {
		return fmt.Errorf("project metadata is nil")
	}
	e.ProjectMeta = meta
	return config.SaveProjectMetadata(e.Env, meta)
}
func(e *Executor) getClient() (*ssh.Client, error) {
	if e.Client != nil {
		return e.Client, nil
	}
	client, err := ssh.NewClient(e.Server.Host, e.Server.Port, e.Server.User, e.Server.KeyPath)
	if err != nil {
		return nil, err
	}
	e.Client = client
	return client, nil
}