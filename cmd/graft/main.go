package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/skssmd/graft/cmd/graft/executors"
	"github.com/skssmd/graft/internal/config"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}
	e := executors.GetExecutor()
	args := os.Args[1:]

	// Handle version and help flags
	if len(args) > 0 {
		arg := args[0]
		if arg == "-v" || arg == "--version" {
			fmt.Println("v2.3.1")
			return
		}
		if arg == "--help" {
			printUsage()
			return
		}
	}

	// Handle target registry flag: graft -r registryname ...
	var registryContext string
	if args[0] == "-r" || args[0] == "--registry" {
		if len(args) < 2 {
			fmt.Println("Usage: graft -r <registryname> <command>")
			return
		}
		registryContext = args[1]
		args = args[2:]

		// Handle shell directly after -r: graft -r name -sh ...
		if len(args) > 0 && (args[0] == "-sh" || args[0] == "--sh") {
			e.RunRegistryShell(registryContext, args[1:])
			return
		}
	}

	// Handle project context flag: graft -p projectname ...
	if args[0] == "-p" || args[0] == "--project" {
		if len(args) < 3 {
			fmt.Println("Usage: graft -p <projectname> <command>")
			return
		}
		projectName := args[1]
		args = args[2:]

		// Lookup project path
		gCfg, _ := config.LoadGlobalConfig()
		if gCfg == nil || gCfg.Projects == nil || gCfg.Projects[projectName] == "" {
			fmt.Printf("Error: Project '%s' not found in global registry\n", projectName)
			return
		}

		projectPath := gCfg.Projects[projectName]
		if err := os.Chdir(projectPath); err != nil {
			fmt.Printf("Error: Could not enter project directory: %v\n", err)
			return
		}
		fmt.Printf("📂 Context: %s (%s)\n", projectName, projectPath)
	}
	e.Env = "prod"

	// Load project metadata and fetch server from global registry for default prod environment
	projectmeta, err := config.LoadProjectMetadata("prod")
	if err == nil && projectmeta != nil && projectmeta.Registry != "" {
		gCfg, _ := config.LoadGlobalConfig()
		if gCfg != nil {
			server := gCfg.Servers[projectmeta.Registry]
			e.Server = server
		}
	}

	if args[0] == "env" {
		//handle wrong input
		if len(args) < 2 {
			fmt.Println("Usage: graft env <command>")
			fmt.Println("Usage: graft -p <projectname> env <envname> <command>")
			fmt.Println("")
			fmt.Println("Usage: graft env --new <envname>")
			fmt.Println("Usage: graft -p <projectname> env --new <envname>")
			return
		}
		//handle new env
		if args[1] == "--new" {
			name := args[2]
			
			if strings.HasSuffix(strings.ToLower(name), "prod")  {
				fmt.Println("Error: Cannot create env named prod")
				return
			}
			e.RunNewEnv(name)
			return
		}
		env := args[1]
		args = args[2:]
		//load project metadata
		projectmeta, err := config.LoadProjectMetadata(env)
		if err != nil {
			fmt.Printf("❌ Error: Environment '%s' not found for this project.\n", env)
			
			// Show available environments
			projEnv, err := config.LoadProjectEnv()
			if err == nil && projEnv != nil {
				fmt.Println("\n📋 Available environments:")
				for eName := range projEnv.Env {
					fmt.Printf("  - %s\n", eName)
				}
				fmt.Println("\n💡 Use 'graft env --new <name>' to create a new environment.")
			}
			return
		}

		e.Env = env
		servername := projectmeta.Registry
		gCfg, _ := config.LoadGlobalConfig()

		server := gCfg.Servers[servername]

		e.Server = server
		fmt.Println(e)
	}
	command := args[0]

	switch command {
	case "init":
		e.RunInit(args[1:])
	case "hook":
		if args[1]=="map"{
			e.RunHookMap()
			return
		}else{
e.RunHook(args[1:])
		}
		
	case "host":
		if len(args) < 2 {
			fmt.Println("Usage: graft host [init|clean|sh|self-destruct]")
			return
		}
		switch args[1] {
		case "init":
			e.RunHostInit()
		case "clean":
			e.RunHostClean()
		case "sh", "-sh", "--sh":
			e.RunHostShell(args[2:])
		case "self-destruct":
			e.RunHostSelfDestruct()
		default:
			fmt.Println("Usage: graft host [init|clean|sh|self-destruct]")
		}
	case "db":
		if len(args) < 3 || args[2] != "init" {
			fmt.Println("Usage: graft db <name> init")
			return
		}
		e.RunInfraInit("postgres", args[1])
	case "redis":
		if len(args) < 3 || args[2] != "init" {
			fmt.Println("Usage: graft redis <name> init")
			return
		}
		e.RunInfraInit("redis", args[1])
	case "infra":
		if len(args) < 2 {
			fmt.Println("Usage: graft infra [db|redis] ports:<value> | graft infra reload")
			return
		}
		if args[1] == "reload" {
			e.RunInfraReload()
		} else {
			e.RunInfra(args[1:])
		}
	case "logs":
		if len(args) < 2 {
			fmt.Println("Usage: graft logs <service>")
			return
		}
		e.RunLogs(args[1])
	case "sync":
		// Check if "compose" subcommand is specified
		if len(args) > 1 && args[1] == "compose" {
			e.RunSyncCompose(args[1:])
		} else {
			e.RunSync(args[1:])
		}
	case "rollback":
		if len(args) > 1 && args[1] == "config" {
			e.RunRollbackConfig()
		} else if len(args) > 1 && args[1] == "service" {
			if len(args) < 3 {
				fmt.Println("Usage: graft rollback service <service-name>")
				return
			}
			e.RunServiceRollback(args[2])
		} else {
			e.RunRollback()
		}
	case "registry":
		if len(args) < 2 {
			fmt.Println("Usage: graft registry [ls|add|del]")
			return
		}
		switch args[1] {
		case "ls":
			e.RunRegistryLs()
		case "add":
			e.RunRegistryAdd()
		case "del":
			if len(args) < 3 {
				fmt.Println("Usage: graft registry del <name>")
				return
			}
			e.RunRegistryDel(args[2])
		default:
			fmt.Println("Usage: graft registry [ls|add|del]")
		}
	case "projects":
		if len(args) > 1 && args[1] == "ls" {
			e.RunProjectsLs(registryContext)
		} else {
			fmt.Println("Usage: graft projects ls")
		}
	case "pullfromhost":
		if registryContext == "" {
			fmt.Println("Error: Pulling requires a registry context. Use 'graft -r <registry> pull <project>'")
			return
		}
		if len(args) < 2 {
			fmt.Println("Usage: graft -r <registry> pullfromhost <project>")
			return
		}
		e.RunPull(registryContext, args[1])
	case "mode":
		e.RunMode()
	case "map":
		if len(args) < 2 {
			e.RunMap([]string{}) // Map all services
		} else if args[1] == "service" {
			if len(args) < 3 {
				fmt.Println("Usage: graft map service <service-name>")
				return
			}
			e.RunMapService(args[2])
		} else {
			e.RunMap(args[1:])
		}
	default:
		// Handle the --pull flag as requested in the specific format
		// foundPull := false
		// for i, arg := range os.Args {
		// 	if arg == "--pull" && i+1 < len(os.Args) {
		// 		if registryContext == "" {
		// 			fmt.Println("Error: Pulling requires a registry context. Use 'graft -r <registry> --pull <project>'")
		// 			return
		// 		}
		// 		runPull(registryContext, os.Args[i+1])
		// 		foundPull = true
		// 		break
		// 	}
		// }
		// if foundPull { return }

		// Pass through to docker compose for any other command
		e.RunDockerCompose(args)
	}
}

func printUsage() {
	fmt.Println("Graft CLI - Interactive Deployment Tool")
	fmt.Println("\nUsage:")
	fmt.Println("  graft [flags] <command> [args]")
	fmt.Println("\nFlags:")
	fmt.Println("  -p, --project <name>      Run command in specific project context")
	fmt.Println("  -r, --registry <name>     Target a specific server context")
	fmt.Println("  -sh, --sh [cmd]           Execute shell command on target (or start SSH session)")
	fmt.Println("  -v, --version             Show version information")
	fmt.Println("  --help                    Show this help message")
	fmt.Println("\nCommands:")
	fmt.Println("  init [-f]                 Initialize a new project")
	fmt.Println("  registry [ls|add|del]     Manage registered servers")
	fmt.Println("  projects ls               List local projects")
	fmt.Println("  pull <project>            Pull/Clone project from remote")
	fmt.Println("  host [init|clean|sh|self-destruct]  Manage current project's host context")
	fmt.Println("  infra [db|redis] ports:<v> Change infra port mapping (null to hide)")
	fmt.Println("  infra db backup           Setup automated database backups to S3")
	fmt.Println("  infra reload              Pull and reload infrastructure services")
	fmt.Println("  db/redis <name> init      Initialize shared infrastructure")
	fmt.Println("  sync [service] [-h]       Deploy project to server")
	fmt.Println("  rollback                  Restore project to a previous backup")
	fmt.Println("  rollback service <name>   Restore specific service from a backup")
	fmt.Println("  rollback config           Configure rollback versions to keep")
	fmt.Println("  logs <service>            Stream service logs")
	fmt.Println("  mode                      Change project deployment mode")
	fmt.Println("  map                       Map all service domains to Cloudflare DNS")
	fmt.Println("  map service <name>        Map specific service domain to Cloudflare DNS")
	fmt.Println("\nFull Documentation:")
	fmt.Println("  https://graftdocs.vercel.app")
}
