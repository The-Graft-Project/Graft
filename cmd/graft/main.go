package main

import (
	"fmt"
	"os"
	"strconv"
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
			fmt.Println("v2.5.4")
			return
		}
		if arg == "--help" {
			printUsage()
			return
		}
		if arg == "pub" {
			if len(args) > 1 && args[1] == "rollout" {
				e.RolloutSSHKeys()
			} else {
				e.GetSSHPub()
			}
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
		// Handle psql directly after -r: graft -r name psql [dbname] [flags...]
		if len(args) > 0 && args[0] == "psql" {
			gCfg, _ := config.LoadGlobalConfig()
			if gCfg != nil {
				srv := gCfg.Servers[registryContext]
				e.Server = &srv
			}
			e.RunPsql("", args[1:])
			return
		}
		// Handle db/redis on registry: graft -r name db <dbname> [init|serve]
		if len(args) > 0 && (args[0] == "db" || args[0] == "redis") {
			gCfg, _ := config.LoadGlobalConfig()
			if gCfg != nil {
				srv := gCfg.Servers[registryContext]
				e.Server = &srv
			}
			if len(args) < 3 {
				fmt.Printf("Usage: graft -r <registry> %s <name> [init|serve]\n", args[0])
				return
			}
			switch args[2] {
			case "init":
				typ := "postgres"
				if args[0] == "redis" {
					typ = "redis"
				}
				e.RunInfraInit(typ, args[1])
			case "serve":
				if args[0] == "redis" {
					fmt.Println("Error: serve is only supported for postgres databases.")
					return
				}
				port := 5432
				if len(args) > 3 {
					p, err := strconv.Atoi(strings.TrimPrefix(args[3], ":"))
					if err == nil && p > 0 {
						port = p
					}
				}
				e.RunDbServe(args[1], port)
			default:
				fmt.Printf("Usage: graft -r <registry> %s <name> [init|serve]\n", args[0])
			}
			return
		}
		// Handle tunnel on registry: graft -r name tunnel <container> [:localport]
		if len(args) > 0 && args[0] == "tunnel" {
			gCfg, _ := config.LoadGlobalConfig()
			if gCfg != nil {
				srv := gCfg.Servers[registryContext]
				e.Server = &srv
			}
			if len(args) < 2 {
				fmt.Println("Usage: graft -r <registry> tunnel <container> [:localport]")
				return
			}
			port := 0
			if len(args) > 2 {
				p, err := strconv.Atoi(strings.TrimPrefix(args[2], ":"))
				if err == nil && p > 0 {
					port = p
				}
			}
			e.RunTunnel(args[1], port)
			return
		}
		// Handle shell directly after -r: graft -r name -sh ...
		if len(args) > 0 {

			e.RunRegistryDocker(registryContext, args)
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
			e.Server = &server
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
		//handle env ls
		if args[1] == "ls" {
			e.RunEnvLs()
			return
		}
		//handle new env
		if args[1] == "--new" {
			name := args[2]

			if strings.HasSuffix(strings.ToLower(name), "prod") {
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

		e.Server = &server
		fmt.Println(e)
	}
	command := args[0]

	switch command {
	case "init":
		e.RunInit(args[1:])
	case "hook":
		if args[1] == "map" {
			e.RunHookMap()
			return
		} else {
			e.RunHook(args[1:])
		}

	case "host":
		if len(args) < 2 {
			fmt.Println("Usage: graft host [init|clean|sh|self-destruct|db|redis]")
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
		case "db":
			if len(args) < 4 {
				fmt.Println("Usage: graft host db <name> [init|serve]")
				return
			}
			switch args[3] {
			case "init":
				e.RunInfraInit("postgres", args[2])
			case "serve":
				port := 5432
				if len(args) > 4 {
					p, err := strconv.Atoi(strings.TrimPrefix(args[4], ":"))
					if err == nil && p > 0 {
						port = p
					}
				}
				e.RunDbServe(args[2], port)
			default:
				fmt.Println("Usage: graft host db <name> [init|serve]")
			}
		case "redis":
			if len(args) < 4 || args[3] != "init" {
				fmt.Println("Usage: graft host redis <name> init")
				return
			}
			e.RunInfraInit("redis", args[2])
		case "tunnel":
			if len(args) < 3 {
				fmt.Println("Usage: graft host tunnel <container> [:localport]")
				return
			}
			port := 0
			if len(args) > 3 {
				p, err := strconv.Atoi(strings.TrimPrefix(args[3], ":"))
				if err == nil && p > 0 {
					port = p
				}
			}
			e.RunTunnel(args[2], port)
		default:
			e.RunHostDocker(args[1:])
		}
	case "db":
		if len(args) < 3 {
			fmt.Println("Usage: graft db <name> init")
			fmt.Println("       graft db <name> serve [:port]")
			return
		}
		switch args[2] {
		case "init":
			e.RunInfraInit("postgres", args[1])
		case "serve":
			port := 5432
			if len(args) > 3 {
				p, err := strconv.Atoi(strings.TrimPrefix(args[3], ":"))
				if err == nil && p > 0 {
					port = p
				}
			}
			var dbOverride string
			if meta, err := config.LoadProjectMetadata(e.Env); err == nil && meta != nil && meta.Database != "" {
				dbOverride = meta.Database
			}
			e.RunDbServe(dbOverride, port)
		default:
			fmt.Println("Usage: graft db <name> init")
			fmt.Println("       graft db <name> serve [:port]")
		}
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
	case "scale":
		if len(args) < 3 {
			fmt.Println("Usage: graft scale <service> <replicas>")
			return
		}
		n, err := strconv.Atoi(args[2])
		if err != nil || n < 1 {
			fmt.Println("Error: replica count must be a positive integer (use 1 to remove replicas)")
			return
		}
		e.RunScale(args[1], n)
	case "psql":
		var dbOverride string
		if meta, err := config.LoadProjectMetadata(e.Env); err == nil && meta != nil && meta.Database != "" {
			dbOverride = meta.Database
		}
		e.RunPsql(dbOverride, args[1:])
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
	fmt.Println("  pub [rollout]             Manage Graft SSH keys (show public key or rotate)")
	fmt.Println("  projects ls               List local projects")
	fmt.Println("  pull <project>            Pull/Clone project from remote")
	fmt.Println("  host [init|clean|sh|self-destruct]  Manage current project's host context")
	fmt.Println("  infra [db|redis] ports:<v> Change infra port mapping (null to hide)")
	fmt.Println("  infra db backup           Setup automated database backups to S3")
	fmt.Println("  infra reload              Pull and reload infrastructure services")
	fmt.Println("  db <name> init            Initialize postgres database (dedicated credentials per db)")
	fmt.Println("  db <name> serve [:port]   Tunnel remote postgres to localhost via SSH (default :5432)")
	fmt.Println("  redis <name> init         Initialize redis database")
	fmt.Println("  sync [service] [-h]       Deploy project to server")
	fmt.Println("  rollback                  Restore project to a previous backup")
	fmt.Println("  rollback service <name>   Restore specific service from a backup")
	fmt.Println("  rollback config           Configure rollback versions to keep")
	fmt.Println("  logs <service>            Stream service logs")
	fmt.Println("  env ls                    List all environments with their registry and domain")
	fmt.Println("  env --new <name>          Create a new deployment environment")
	fmt.Println("  env <name> <command>      Run a command in a specific environment context")
	fmt.Println("  scale <service> <n>       Scale a service to N replicas via Traefik load balancing (1 = remove replicas)")
	fmt.Println("  host tunnel <c> [:port]   Tunnel any Docker container to localhost via SSH")
	fmt.Println("  psql [-c \"cmd\"]           Open psql session or run one-off SQL on infra postgres")
	fmt.Println("  mode                      Change project deployment mode")
	fmt.Println("  map                       Map all service domains to Cloudflare DNS")
	fmt.Println("  map service <name>        Map specific service domain to Cloudflare DNS")
	fmt.Println("\nFull Documentation:")
	fmt.Println("  https://graftdocs.vercel.app")
}
