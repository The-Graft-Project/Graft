package deploy

import (
	"fmt"
	"strings"
)

const scaleReplicaSuffix = "-scale-"

func isScaleReplica(name string) bool {
	return strings.Contains(name, scaleReplicaSuffix)
}

// InjectScaleReplicas removes stale replicas and adds fresh ones based on the scale map.
// Replicas share only the Traefik loadbalancer service label with the primary so Traefik
// round-robins traffic between them automatically.
func InjectScaleReplicas(compose *DockerComposeFile, scale map[string]int) {
	if compose == nil {
		return
	}

	// Remove any existing scale replicas first (handles scale-down and renames)
	for name := range compose.Services {
		if isScaleReplica(name) {
			delete(compose.Services, name)
		}
	}

	for serviceName, count := range scale {
		if count <= 1 {
			continue
		}
		primary, ok := compose.Services[serviceName]
		if !ok {
			continue
		}

		traefikSvcName, traefikPort := extractTraefikServiceInfo(serviceName, primary.Labels)

		for i := 2; i <= count; i++ {
			replicaName := fmt.Sprintf("%s%s%d", serviceName, scaleReplicaSuffix, i)
			replica := ComposeService{
				Image:    primary.Image,
				EnvFiles: primary.EnvFiles,
				OtherFields: map[string]interface{}{},
			}

			// Carry over networks and volumes; skip container_name and ports (would conflict)
			for k, v := range primary.OtherFields {
				if k == "container_name" || k == "ports" {
					continue
				}
				replica.OtherFields[k] = v
			}

			if traefikPort != "" {
				replica.Labels = []string{
					fmt.Sprintf("traefik.http.services.%s.loadbalancer.server.port=%s", traefikSvcName, traefikPort),
				}
			}

			compose.Services[replicaName] = replica
		}
	}
}

// extractTraefikServiceInfo finds the Traefik service name and loadbalancer port from labels.
func extractTraefikServiceInfo(composeSvcName string, labels []string) (svcName, port string) {
	svcName = composeSvcName
	for _, label := range labels {
		parts := strings.SplitN(label, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, val := parts[0], parts[1]

		// traefik.http.routers.<r>.service=<svcname>
		if strings.Contains(key, "traefik.http.routers.") && strings.HasSuffix(key, ".service") {
			svcName = val
		}
		// traefik.http.services.<svcname>.loadbalancer.server.port=<port>
		if strings.Contains(key, "traefik.http.services.") && strings.HasSuffix(key, ".loadbalancer.server.port") {
			port = val
		}
	}
	return svcName, port
}
