package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/stratonmesh/stratonmesh/pkg/manifest"
	"github.com/stratonmesh/stratonmesh/pkg/orchestrator"
	"go.uber.org/zap"
)

// Adapter implements the PlatformAdapter interface for Docker Engine.
type Adapter struct {
	client *client.Client
	logger *zap.SugaredLogger
}

var _ orchestrator.PlatformAdapter = (*Adapter)(nil)

// New creates a Docker adapter connected to the local Docker daemon.
func New(logger *zap.SugaredLogger) (*Adapter, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("connect to docker: %w", err)
	}
	// Verify connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cli.Ping(ctx); err != nil {
		return nil, fmt.Errorf("docker ping: %w", err)
	}
	logger.Info("docker adapter connected")
	return &Adapter{client: cli, logger: logger}, nil
}

func (a *Adapter) Name() string { return "docker" }

// Generate produces a deployment plan (for preview/logging).
func (a *Adapter) Generate(ctx context.Context, stack *manifest.Stack) ([]byte, error) {
	var plan strings.Builder
	plan.WriteString(fmt.Sprintf("# Docker deployment plan for %s v%s\n\n", stack.Name, stack.Version))

	netName := fmt.Sprintf("%s-net", stack.Name)
	plan.WriteString(fmt.Sprintf("docker network create %s\n\n", netName))

	for _, svc := range stack.Services {
		if !svc.IsEnabled() {
			continue
		}
		replicas := svc.DefaultReplicas()
		for i := 0; i < replicas; i++ {
			containerName := fmt.Sprintf("%s-%s-%d", stack.Name, svc.Name, i)
			plan.WriteString(fmt.Sprintf("docker run -d --name %s --network %s", containerName, netName))
			for _, p := range svc.Ports {
				plan.WriteString(fmt.Sprintf(" -p %d", p.Expose))
			}
			for k, v := range svc.Env {
				plan.WriteString(fmt.Sprintf(" -e %s=%s", k, v))
			}
			plan.WriteString(fmt.Sprintf(" %s\n", svc.Image))
		}
		plan.WriteString("\n")
	}

	return []byte(plan.String()), nil
}

// Apply executes the deployment — creates network, volumes, and containers.
func (a *Adapter) Apply(ctx context.Context, stack *manifest.Stack) error {
	netName := fmt.Sprintf("%s-net", stack.Name)

	// Create network (idempotent)
	if err := a.ensureNetwork(ctx, netName); err != nil {
		return fmt.Errorf("create network: %w", err)
	}

	// Deploy services in dependency order (already topologically sorted)
	for _, svc := range stack.Services {
		if !svc.IsEnabled() {
			continue
		}

		// Create volumes for stateful services
		for _, vol := range svc.Volumes {
			volName := fmt.Sprintf("%s-%s", stack.Name, vol.Name)
			if err := a.ensureVolume(ctx, volName); err != nil {
				return fmt.Errorf("create volume %s: %w", volName, err)
			}
		}

		// Pull image
		if err := a.pullImage(ctx, svc.Image); err != nil {
			a.logger.Warnw("image pull failed, trying local", "image", svc.Image, "error", err)
		}

		// Create containers for each replica
		replicas := svc.DefaultReplicas()
		for i := 0; i < replicas; i++ {
			containerName := fmt.Sprintf("%s-%s-%d", stack.Name, svc.Name, i)

			if err := a.createAndStart(ctx, containerName, &svc, stack, netName, i); err != nil {
				return fmt.Errorf("deploy %s: %w", containerName, err)
			}
			a.logger.Infow("container started", "name", containerName, "image", svc.Image)
		}
	}

	return nil
}

// Status queries the running state of all containers in a stack.
func (a *Adapter) Status(ctx context.Context, stackID string) (*orchestrator.AdapterStatus, error) {
	// List containers with the stack label
	containers, err := a.client.ContainerList(ctx, container.ListOptions{
		All: true,
	})
	if err != nil {
		return nil, err
	}

	serviceMap := make(map[string]*orchestrator.ServiceStatus)
	prefix := stackID + "-"

	for _, c := range containers {
		for _, name := range c.Names {
			name = strings.TrimPrefix(name, "/")
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			// Parse service name from container name: {stack}-{service}-{replica}
			parts := strings.Split(strings.TrimPrefix(name, prefix), "-")
			if len(parts) < 2 {
				continue
			}
			svcName := strings.Join(parts[:len(parts)-1], "-")

			if _, ok := serviceMap[svcName]; !ok {
				serviceMap[svcName] = &orchestrator.ServiceStatus{Name: svcName}
			}
			ss := serviceMap[svcName]
			ss.Replicas++

			if c.State == "running" {
				ss.Ready++
				ss.Health = "healthy"
			} else {
				ss.Health = "unhealthy"
			}
		}
	}

	var services []orchestrator.ServiceStatus
	for _, ss := range serviceMap {
		services = append(services, *ss)
	}

	return &orchestrator.AdapterStatus{Services: services}, nil
}

// Destroy removes all containers, volumes, and networks for a stack.
func (a *Adapter) Destroy(ctx context.Context, stackID string) error {
	containers, err := a.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return err
	}

	prefix := stackID + "-"
	for _, c := range containers {
		for _, name := range c.Names {
			name = strings.TrimPrefix(name, "/")
			if strings.HasPrefix(name, prefix) {
				timeout := 30 // seconds
				a.client.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
				a.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
				a.logger.Infow("container removed", "name", name)
			}
		}
	}

	// Remove network
	netName := fmt.Sprintf("%s-net", stackID)
	a.client.NetworkRemove(ctx, netName)

	return nil
}

// Diff compares desired state against running containers.
func (a *Adapter) Diff(ctx context.Context, desired, actual *manifest.Stack) (*orchestrator.DiffResult, error) {
	result := &orchestrator.DiffResult{}

	actualServices := make(map[string]*manifest.Service)
	if actual != nil {
		for i := range actual.Services {
			actualServices[actual.Services[i].Name] = &actual.Services[i]
		}
	}

	for _, svc := range desired.Services {
		if !svc.IsEnabled() {
			continue
		}
		existing, ok := actualServices[svc.Name]
		if !ok {
			result.Create = append(result.Create, svc.Name)
			continue
		}

		// Check for changes
		if svc.Image != existing.Image {
			result.Update = append(result.Update, orchestrator.UpdateAction{
				Service: svc.Name, ChangeType: "image", From: existing.Image, To: svc.Image,
			})
		} else if svc.Replicas != existing.Replicas {
			result.Update = append(result.Update, orchestrator.UpdateAction{
				Service: svc.Name, ChangeType: "replicas",
				From: fmt.Sprintf("%d", existing.Replicas), To: fmt.Sprintf("%d", svc.Replicas),
			})
		} else {
			result.Unchanged = append(result.Unchanged, svc.Name)
		}

		delete(actualServices, svc.Name)
	}

	// Remaining actual services are no longer in desired state
	for name := range actualServices {
		result.Destroy = append(result.Destroy, name)
	}

	return result, nil
}

// Rollback destroys current and re-deploys from ledger (handled by orchestrator).
func (a *Adapter) Rollback(ctx context.Context, stackID string, version string) error {
	return a.Destroy(ctx, stackID)
}

// --- Internal helpers ---

func (a *Adapter) ensureNetwork(ctx context.Context, name string) error {
	networks, err := a.client.NetworkList(ctx, dockertypes.NetworkListOptions{})
	if err != nil {
		return err
	}
	for _, n := range networks {
		if n.Name == name {
			return nil // already exists
		}
	}
	_, err = a.client.NetworkCreate(ctx, name, dockertypes.NetworkCreate{
		Driver: "bridge",
	})
	return err
}

func (a *Adapter) ensureVolume(ctx context.Context, name string) error {
	// Docker creates volumes idempotently
	_, err := a.client.VolumeCreate(ctx, volume.CreateOptions{Name: name})
	return err
}

func (a *Adapter) pullImage(ctx context.Context, ref string) error {
	reader, err := a.client.ImagePull(ctx, ref, dockertypes.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer reader.Close()
	io.Copy(io.Discard, reader) // wait for pull to complete
	return nil
}

func (a *Adapter) createAndStart(ctx context.Context, name string, svc *manifest.Service, stack *manifest.Stack, netName string, replicaIdx int) error {
	// Remove existing container if present (idempotent)
	a.client.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})

	// Build environment variables
	var env []string
	for k, v := range svc.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}

	// Build port bindings
	exposedPorts := make(map[string]struct{})
	for _, p := range svc.Ports {
		exposedPorts[fmt.Sprintf("%d/tcp", p.Expose)] = struct{}{}
	}

	// Build volume mounts
	var mounts []mount.Mount
	for _, vol := range svc.Volumes {
		volName := fmt.Sprintf("%s-%s", stack.Name, vol.Name)
		target := vol.MountAt
		if target == "" {
			target = "/data/" + vol.Name
		}
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeVolume,
			Source: volName,
			Target: target,
		})
	}

	// Build health check
	var healthCheck *container.HealthConfig
	if svc.HealthCheck.Liveness != nil && svc.HealthCheck.Liveness.HTTP != "" {
		healthCheck = &container.HealthConfig{
			Test:     []string{"CMD", "curl", "-f", fmt.Sprintf("http://localhost:%d%s", svc.Ports[0].Expose, svc.HealthCheck.Liveness.HTTP)},
			Interval: 10 * time.Second,
			Timeout:  3 * time.Second,
			Retries:  3,
		}
	}

	// Parse resource limits
	var resources container.Resources
	if svc.Resources.CPU != "" {
		cpuMillis := parseCPUMillis(svc.Resources.CPU)
		resources.NanoCPUs = cpuMillis * 1000000 // millicores to nanocores
	}
	if svc.Resources.Memory != "" {
		resources.Memory = parseMemBytes(svc.Resources.Memory)
	}

	// Build labels for service discovery
	labels := map[string]string{
		"stratonmesh.stack":   stack.Name,
		"stratonmesh.service": svc.Name,
		"stratonmesh.version": stack.Version,
		"stratonmesh.replica": fmt.Sprintf("%d", replicaIdx),
	}

	// Build command
	var cmd []string
	if svc.Command != "" {
		cmd = strings.Fields(svc.Command)
	}

	resp, err := a.client.ContainerCreate(ctx,
		&container.Config{
			Image:        svc.Image,
			Env:          env,
			ExposedPorts: toPortSet(exposedPorts),
			Healthcheck:  healthCheck,
			Labels:       labels,
			Cmd:          cmd,
		},
		&container.HostConfig{
			RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
			Mounts:        mounts,
			Resources:     resources,
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				netName: {},
			},
		},
		nil,
		name,
	)
	if err != nil {
		return fmt.Errorf("create container %s: %w", name, err)
	}

	if err := a.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start container %s: %w", name, err)
	}

	return nil
}

func toPortSet(ports map[string]struct{}) nat.PortSet {
	ps := make(nat.PortSet, len(ports))
	for p := range ports {
		ps[nat.Port(p)] = struct{}{}
	}
	return ps
}

func parseCPUMillis(s string) int64 {
	var val int64
	if _, err := fmt.Sscanf(s, "%dm", &val); err == nil {
		return val
	}
	return 500
}

func parseMemBytes(s string) int64 {
	var val int64
	if _, err := fmt.Sscanf(s, "%dGi", &val); err == nil {
		return val * 1024 * 1024 * 1024
	}
	if _, err := fmt.Sscanf(s, "%dMi", &val); err == nil {
		return val * 1024 * 1024
	}
	return 256 * 1024 * 1024
}
