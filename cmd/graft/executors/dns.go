package executors

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/deploy"
	"github.com/skssmd/graft/internal/dns"
	"github.com/skssmd/graft/internal/ssh"
)

func (e *Executor) RunMap(args []string) {
	reader := bufio.NewReader(os.Stdin)

	// Load project config
	cfg := e

	// Parse graft-compose.yml
	compose, err := deploy.ParseComposeFile("graft-compose.yml")
	if err != nil {
		fmt.Printf("Error: Failed to parse graft-compose.yml: %v\n", err)
		return
	}

	// Extract all services with domains
	type ServiceDomains struct {
		Service string
		Domains []string
	}
	var serviceDomains []ServiceDomains

	fmt.Println("🔍 Parsing docker-compose file...")
	for serviceName, service := range compose.Services {
		hosts := deploy.ExtractTraefikHosts(service.Labels)
		if len(hosts) > 0 {
			serviceDomains = append(serviceDomains, ServiceDomains{
				Service: serviceName,
				Domains: hosts,
			})
		}
	}

	if len(serviceDomains) == 0 {
		fmt.Println("❌ No services with Traefik Host labels found in graft-compose.yml")
		return
	}

	// Display found services
	fmt.Printf("📋 Found %d service(s) with domains:\n", len(serviceDomains))
	for _, sd := range serviceDomains {
		fmt.Printf("  - %s: %s\n", sd.Service, strings.Join(sd.Domains, ", "))
	}

	// Get server IP
	fmt.Println("\n🌐 Detecting server IP...")
	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: Could not connect to server: %v\n", err)
		return
	}
	defer client.Close()

	// Get public IP from server
	var ipOutput strings.Builder
	err = client.RunCommand("curl -s https://api.ipify.org", &ipOutput, os.Stderr)
	serverIP := strings.TrimSpace(ipOutput.String())

	if err != nil || serverIP == "" {
		fmt.Print("Could not auto-detect server IP. Enter manually: ")
		input, _ := reader.ReadString('\n')
		serverIP = strings.TrimSpace(input)
	} else {
		fmt.Printf("Detected IP: %s\n", serverIP)
	}
	cloudflare, err := config.LoadCloudFlareConfig()
	if err != nil {
		fmt.Println("failed to load cloudflare config")
	}

	// Get Cloudflare credentials
	apiToken, zoneID := fetchCloudflareCredentials(cloudflare, reader)
	if apiToken == "" || zoneID == "" {
		fmt.Println("❌ Cloudflare API Token and Zone ID are required.")
		return
	}

	// Verify DNS ownership
	fmt.Println("\n🔐 Verifying DNS ownership...")
	verified, err := dns.VerifyDNSOwnership("", apiToken, zoneID)
	if err != nil || !verified {
		fmt.Printf("❌ DNS ownership verification failed: %v\n", err)
		return
	}
	fmt.Println("✅ DNS ownership verified")

	// Process each domain
	fmt.Println("\n📍 Checking DNS records...")

	stats := struct {
		unchanged int
		updated   int
		created   int
		skipped   int
	}{}

	for _, sd := range serviceDomains {
		for _, domain := range sd.Domains {
			// Get existing record
			record, err := dns.GetDNSRecord(domain, "", apiToken, zoneID)

			if err != nil {
				fmt.Printf("  ❌ Error checking %s: %v\n", domain, err)
				stats.skipped++
				continue
			}

			if record != nil {
				// Record exists
				if record.Content == serverIP {
					fmt.Printf("  ✅ %s → %s (already correct)\n", domain, serverIP)
					stats.unchanged++
				} else {
					fmt.Printf("  ⚠️  %s → %s (exists, current: %s)\n", domain, serverIP, record.Content)
					fmt.Printf("      Overwrite with %s? (y/n): ", serverIP)
					confirm, _ := reader.ReadString('\n')
					confirm = strings.ToLower(strings.TrimSpace(confirm))

					if confirm == "y" || confirm == "yes" {
						err = dns.UpdateDNSRecord(record.ID, serverIP, apiToken, zoneID)
						if err != nil {
							fmt.Printf("      ❌ Failed to update: %v\n", err)
							stats.skipped++
						} else {
							fmt.Printf("      ✅ Updated\n")
							stats.updated++
						}
					} else {
						fmt.Printf("      ⏭️  Skipped\n")
						stats.skipped++
					}
				}
			} else {
				// Record doesn't exist, create it
				fmt.Printf("  ➕ %s → Creating new record...\n", domain)
				err = dns.CreateDNSRecord(domain, "", serverIP, apiToken, zoneID)
				if err != nil {
					fmt.Printf("      ❌ Failed to create: %v\n", err)
					stats.skipped++
				} else {
					fmt.Printf("      ✅ Created\n")
					stats.created++
				}
			}
		}
	}

	// Display summary
	fmt.Println("\n✅ DNS mapping complete!")
	if stats.unchanged > 0 {
		fmt.Printf("  - %d unchanged\n", stats.unchanged)
	}
	if stats.updated > 0 {
		fmt.Printf("  - %d updated\n", stats.updated)
	}
	if stats.created > 0 {
		fmt.Printf("  - %d created\n", stats.created)
	}
	if stats.skipped > 0 {
		fmt.Printf("  - %d skipped\n", stats.skipped)
	}
}

func (e *Executor) RunMapService(serviceName string) {
	reader := bufio.NewReader(os.Stdin)

	// Load project config
	cfg := e

	// Parse graft-compose.yml
	compose, err := deploy.ParseComposeFile("graft-compose.yml")
	if err != nil {
		fmt.Printf("Error: Failed to parse graft-compose.yml: %v\n", err)
		return
	}

	// Find the service
	service, exists := compose.Services[serviceName]
	if !exists {
		fmt.Printf("❌ Service '%s' not found in graft-compose.yml\n", serviceName)
		return
	}

	// Extract domains
	hosts := deploy.ExtractTraefikHosts(service.Labels)
	if len(hosts) == 0 {
		fmt.Printf("❌ Service '%s' has no Traefik Host labels\n", serviceName)
		return
	}

	fmt.Printf("🔍 Mapping service: %s\n", serviceName)
	fmt.Printf("📋 Found domain(s): %s\n", strings.Join(hosts, ", "))

	// Get server IP
	fmt.Println("\n🌐 Detecting server IP...")
	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: Could not connect to server: %v\n", err)
		return
	}
	defer client.Close()

	// Get public IP from server
	var ipOutput strings.Builder
	err = client.RunCommand("curl -s https://api.ipify.org", &ipOutput, os.Stderr)
	serverIP := strings.TrimSpace(ipOutput.String())

	if err != nil || serverIP == "" {
		fmt.Print("Could not auto-detect server IP. Enter manually: ")
		input, _ := reader.ReadString('\n')
		serverIP = strings.TrimSpace(input)
	} else {
		fmt.Printf("Server IP: %s\n", serverIP)
	}
	cloudflare, err := config.LoadCloudFlareConfig()
	// Get Cloudflare credentials
	apiToken, zoneID := fetchCloudflareCredentials(cloudflare, reader)
	if apiToken == "" || zoneID == "" {
		fmt.Println("❌ Cloudflare API Token and Zone ID are required.")
		return
	}

	// Verify DNS ownership
	fmt.Println("\n🔐 Verifying DNS ownership...")
	verified, err := dns.VerifyDNSOwnership("", apiToken, zoneID)
	if err != nil || !verified {
		fmt.Printf("❌ DNS ownership verification failed: %v\n", err)
		return
	}
	fmt.Println("✅ DNS ownership verified")

	// Process each domain
	fmt.Println("\n📍 Processing DNS records...")

	for _, domain := range hosts {
		// Get existing record
		record, err := dns.GetDNSRecord(domain, "", apiToken, zoneID)

		if err != nil {
			fmt.Printf("❌ Error checking %s: %v\n", domain, err)
			continue
		}

		if record != nil {
			// Record exists
			if record.Content == serverIP {
				fmt.Printf("✅ %s → %s (already correct)\n", domain, serverIP)
			} else {
				fmt.Printf("⚠️  %s → %s (exists, current: %s)\n", domain, serverIP, record.Content)
				fmt.Printf("    Overwrite with %s? (y/n): ", serverIP)
				confirm, _ := reader.ReadString('\n')
				confirm = strings.ToLower(strings.TrimSpace(confirm))

				if confirm == "y" || confirm == "yes" {
					err = dns.UpdateDNSRecord(record.ID, serverIP, apiToken, zoneID)
					if err != nil {
						fmt.Printf("    ❌ Failed to update: %v\n", err)
					} else {
						fmt.Printf("    ✅ Updated\n")
					}
				} else {
					fmt.Printf("    ⏭️  Skipped\n")
				}
			}
		} else {
			// Record doesn't exist, create it
			fmt.Printf("➕ %s → Creating new record...\n", domain)
			err = dns.CreateDNSRecord(domain, "", serverIP, apiToken, zoneID)
			if err != nil {
				fmt.Printf("    ❌ Failed to create: %v\n", err)
			} else {
				fmt.Printf("    ✅ Created: %s → %s\n", domain, serverIP)
			}
		}
	}

	fmt.Println("\n✅ DNS mapping complete for service:", serviceName)
}

func fetchCloudflareCredentials(cfg *config.Cloudflare, reader *bufio.Reader) (string, string) {
	// 1. Try environment variables
	envToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	envZone := os.Getenv("CLOUDFLARE_ZONE_ID")
	if envToken != "" && envZone != "" {
		fmt.Println("🔑 Using Cloudflare credentials from environment variables")
		return envToken, envZone
	}

	// 2. Load all available accounts
	accounts := cfg.CloudflareAccounts
	if accounts == nil {
		accounts = make(map[string]config.CloudflareConfig)
	}

	// Also add local/legacy ones if they exist and aren't in the map
	if cfg.Cloudflare.APIToken != "" && cfg.Cloudflare.ZoneID != "" {
		name := cfg.Cloudflare.Domain
		if name == "" {
			name = "Local Config"
		}
		accounts[name] = cfg.Cloudflare
	}

	for {
		if len(accounts) == 0 {
			fmt.Println("\n🔐 No Cloudflare accounts saved.")
			return promptNewAccount(reader)
		}

		fmt.Println("\n🔐 Cloudflare Account Selection:")

		var domains []string
		for domain := range accounts {
			domains = append(domains, domain)
		}
		sort.Strings(domains)

		for i, domain := range domains {
			acc := accounts[domain]
			fmt.Printf("  %d. %s (%s)\n", i+1, domain, acc.ZoneID)
		}
		fmt.Printf("  /new. Add new Cloudflare account\n")

		fmt.Print("\nSelect an account (1, 2, ...) or type /new: ")
		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)

		if choice == "/new" {
			return promptNewAccount(reader)
		}

		// Try numeric selection
		var index int
		_, err := fmt.Sscanf(choice, "%d", &index)
		if err == nil && index > 0 && index <= len(domains) {
			selected := accounts[domains[index-1]]
			fmt.Printf("✅ Using account: %s\n", domains[index-1])
			return selected.APIToken, selected.ZoneID
		}

		fmt.Println("❌ Invalid selection. Please try again.")
	}
}

func promptNewAccount(reader *bufio.Reader) (string, string) {
	fmt.Print("\n🔐 Cloudflare API Token: ")
	token, _ := reader.ReadString('\n')
	token = strings.TrimSpace(token)

	fmt.Print("🔐 Zone ID: ")
	zone, _ := reader.ReadString('\n')
	zone = strings.TrimSpace(zone)

	if token == "" || zone == "" {
		fmt.Println("❌ Token and Zone ID are required.")
		return "", ""
	}

	// Fetch domain name automatically
	fmt.Println("🔍 Fetching domain name from Cloudflare...")
	domain, err := dns.GetZoneDomain(token, zone)
	if err != nil {
		fmt.Printf("⚠️  Could not fetch domain name: %v\n", err)
		fmt.Print("Enter a name for this account: ")
		domain, _ = reader.ReadString('\n')
		domain = strings.TrimSpace(domain)
		if domain == "" {
			domain = zone
		}
	} else {
		fmt.Printf("✅ Found domain: %s\n", domain)
	}

	fmt.Print("💾 Save these credentials globally? (y/n): ")
	saveGlobally, _ := reader.ReadString('\n')
	saveGlobally = strings.ToLower(strings.TrimSpace(saveGlobally))

	if saveGlobally == "y" || saveGlobally == "yes" {
		config.SaveGlobalCloudflare(token, zone, domain)
		fmt.Println("✅ Cloudflare credentials saved globally")
	}

	return token, zone
}
