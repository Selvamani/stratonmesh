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
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"github.com/selvamani/stratonmesh/pkg/manifest"
	"github.com/selvamani/stratonmesh/pkg/orchestrator"
	"github.com/selvamani/stratonmesh/pkg/portalloc"
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
	// Allocate dynamic ports declared via portVars.
	if len(stack.PortVars) > 0 {
		resolved, err := manifest.AllocatePorts(stack, func(start, end int) (int, error) {
			if end > start {
				return portalloc.FindInRange(start, end)
			}
			return portalloc.FindAvailable(start)
		})
		if err != nil {
			return fmt.Errorf("allocate ports: %w", err)
		}
		manifest.InjectResolvedPorts(stack, resolved)
		if len(resolved) > 0 {
			a.logger.Infow("port vars resolved", "stack", stack.Name, "ports", resolved)
		}
	}

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

// Stop pauses all containers in a stack (preserves volumes).
func (a *Adapter) Stop(ctx context.Context, stackID string) error {
	containers, err := a.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return err
	}
	prefix := stackID + "-"
	timeout := 30
	for _, c := range containers {
		for _, name := range c.Names {
			if strings.HasPrefix(strings.TrimPrefix(name, "/"), prefix) {
				if err := a.client.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout}); err != nil {
					a.logger.Warnw("stop container error", "container", name, "error", err)
				}
			}
		}
	}
	return nil
}

// Start resumes all stopped containers in a stack.
func (a *Adapter) Start(ctx context.Context, stackID string) error {
	containers, err := a.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return err
	}
	prefix := stackID + "-"
	for _, c := range containers {
		for _, name := range c.Names {
			if strings.HasPrefix(strings.TrimPrefix(name, "/"), prefix) {
				if c.State != "running" {
					if err := a.client.ContainerStart(ctx, c.ID, container.StartOptions{}); err != nil {
						a.logger.Warnw("start container error", "container", name, "error", err)
					}
				}
			}
		}
	}
	return nil
}

// Restart stops then starts all containers in the stack, preserving volumes.
func (a *Adapter) Restart(ctx context.Context, stackID string) error {
	if err := a.Stop(ctx, stackID); err != nil {
		return err
	}
	return a.Start(ctx, stackID)
}

// Down removes containers and networks for a stack but preserves named volumes.
func (a *Adapter) Down(ctx context.Context, stackID string) error {
	containers, err := a.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return err
	}
	prefix := stackID + "-"
	timeout := 30
	for _, c := range containers {
		for _, name := range c.Names {
			name = strings.TrimPrefix(name, "/")
			if strings.HasPrefix(name, prefix) {
				a.client.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
				// RemoveVolumes: false — keep named volumes intact
				a.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: false})
				a.logger.Infow("container removed (down)", "name", name)
			}
		}
	}
	netName := fmt.Sprintf("%s-net", stackID)
	a.client.NetworkRemove(ctx, netName)
	return nil
}

// Destroy removes all containers, volumes, and networks for a stack.
func (a *Adapter) Destroy(ctx context.Context, stackID string) error {
	containers, err := a.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return err
	}

	prefix := stackID + "-"
	volumeSet := map[string]struct{}{}
	timeout := 30
	for _, c := range containers {
		for _, name := range c.Names {
			name = strings.TrimPrefix(name, "/")
			if strings.HasPrefix(name, prefix) {
				a.client.ContainerStop(ctx, c.ID, container.StopOptions{Timeout: &timeout})
				a.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
				// Collect anonymous volumes for explicit removal
				for _, m := range c.Mounts {
					if m.Name != "" {
						volumeSet[m.Name] = struct{}{}
					}
				}
				a.logger.Infow("container removed", "name", name)
			}
		}
	}

	// Remove named volumes that belonged to this stack
	for volName := range volumeSet {
		if strings.HasPrefix(volName, stackID+"-") {
			if err := a.client.VolumeRemove(ctx, volName, true); err != nil {
				a.logger.Warnw("volume remove error", "volume", volName, "error", err)
			}
		}
	}

	// Remove network
	netName := fmt.Sprintf("%s-net", stackID)
	a.client.NetworkRemove(ctx, netName)

	return nil
}

// Inspect returns detailed runtime info for one service.
func (a *Adapter) Inspect(ctx context.Context, stackID, service string) (*orchestrator.ServiceDetail, error) {
	containers, err := a.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	prefix := fmt.Sprintf("%s-%s-", stackID, service)
	detail := &orchestrator.ServiceDetail{Name: service}

	for _, c := range containers {
		for _, name := range c.Names {
			name = strings.TrimPrefix(name, "/")
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			// Full inspect for the first matching container (to get image, env, mounts, etc.)
			if detail.Image == "" {
				info, _, err := a.client.ContainerInspectWithRaw(ctx, c.ID, false)
				if err == nil {
					detail.Image = info.Config.Image
					detail.Command = strings.Join(info.Config.Cmd, " ")
					detail.Created = info.Created
					detail.Env = info.Config.Env
					detail.Labels = info.Config.Labels
					for _, m := range info.Mounts {
						detail.Mounts = append(detail.Mounts, orchestrator.MountInfo{
							Type:        string(m.Type),
							Source:      m.Source,
							Destination: m.Destination,
							Mode:        m.Mode,
						})
					}
					for cp, bindings := range info.HostConfig.PortBindings {
						for _, hb := range bindings {
							proto := cp.Proto()
							if proto == "" {
								proto = "tcp"
							}
							detail.Ports = append(detail.Ports, orchestrator.PortBinding{
								HostIP:        hb.HostIP,
								HostPort:      hb.HostPort,
								ContainerPort: cp.Port(),
								Protocol:      proto,
							})
						}
					}
				}
			}
			status := c.State
			health := "unknown"
			if c.Status != "" {
				if strings.Contains(c.Status, "healthy") {
					health = "healthy"
				} else if strings.Contains(c.Status, "unhealthy") {
					health = "unhealthy"
				} else if c.State == "running" {
					health = "running"
				}
			}
			detail.Instances = append(detail.Instances, orchestrator.InstanceInfo{
				ID:      c.ID[:12],
				Name:    name,
				Status:  status,
				Health:  health,
				Started: c.Status,
			})
		}
	}
	return detail, nil
}

// Logs returns recent log lines for a service.
func (a *Adapter) Logs(ctx context.Context, stackID, service string, tail int) (string, error) {
	if tail <= 0 {
		tail = 100
	}
	containers, err := a.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return "", err
	}
	prefix := fmt.Sprintf("%s-%s-", stackID, service)
	var allLogs strings.Builder
	for _, c := range containers {
		for _, name := range c.Names {
			name = strings.TrimPrefix(name, "/")
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			tailStr := fmt.Sprintf("%d", tail)
			opts := container.LogsOptions{
				ShowStdout: true,
				ShowStderr: true,
				Tail:       tailStr,
				Timestamps: true,
			}
			rc, err := a.client.ContainerLogs(ctx, c.ID, opts)
			if err != nil {
				continue
			}
			var buf strings.Builder
			io.Copy(&buf, rc)
			rc.Close()
			if allLogs.Len() > 0 {
				allLogs.WriteString(fmt.Sprintf("\n--- %s ---\n", name))
			}
			allLogs.WriteString(buf.String())
		}
	}
	return allLogs.String(), nil
}

// LogStream opens a live-follow log stream for a service via the Docker SDK.
// The stream is demultiplexed from Docker's header format via stdcopy.
func (a *Adapter) LogStream(ctx context.Context, stackID, service string) (io.ReadCloser, error) {
	containers, err := a.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	prefix := fmt.Sprintf("%s-%s-", stackID, service)
	var containerID string
	for _, c := range containers {
		for _, name := range c.Names {
			if strings.HasPrefix(strings.TrimPrefix(name, "/"), prefix) {
				containerID = c.ID
				break
			}
		}
		if containerID != "" {
			break
		}
	}
	if containerID == "" {
		return nil, fmt.Errorf("no container found for service %q in stack %q", service, stackID)
	}
	raw, err := a.client.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: true,
		Tail:       "50",
	})
	if err != nil {
		return nil, err
	}
	// Docker SDK returns a multiplexed stream; demux stdout+stderr into a single pipe.
	pr, pw := io.Pipe()
	go func() {
		stdcopy.StdCopy(pw, pw, raw) //nolint:errcheck
		raw.Close()
		pw.Close()
	}()
	return pr, nil
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
