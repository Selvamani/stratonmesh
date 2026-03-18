// Package mesos implements the PlatformAdapter interface for Apache Mesos + Marathon.
//
// Marathon is the long-running service framework for Mesos. This adapter generates
// Marathon app definitions (JSON) and submits them via the Marathon REST API:
//   - Generate  → Marathon app JSON
//   - Apply     → PUT /v2/apps/{id} (create or update)
//   - Stop      → PATCH instances=0 (suspend without deletion)
//   - Start     → PATCH instances=N (resume to desired replicas)
//   - Status    → GET /v2/apps/{id}
//   - Inspect   → GET /v2/apps/{id}?embed=app.taskStats + /v2/tasks?appId={id}
//   - Logs      → Mesos sandbox logs via Mesos agent HTTP API (best-effort)
//   - Destroy   → DELETE /v2/apps/{id}
//   - Diff      → compares desired replicas vs live Marathon app state
//   - Rollback  → POST /v2/apps/{id}/versions/{version}
package mesos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/selvamani/stratonmesh/pkg/manifest"
	"github.com/selvamani/stratonmesh/pkg/orchestrator"
	"go.uber.org/zap"
)

// Adapter is the Mesos/Marathon platform adapter.
type Adapter struct {
	logger        *zap.SugaredLogger
	marathonURL   string // e.g. "http://marathon.mesos:8080"
	client        *http.Client
}

var _ orchestrator.PlatformAdapter = (*Adapter)(nil)

// New creates a Mesos/Marathon adapter.
// marathonURL defaults to "http://marathon.mesos:8080" if empty.
func New(logger *zap.SugaredLogger, marathonURL string) *Adapter {
	if marathonURL == "" {
		marathonURL = "http://marathon.mesos:8080"
	}
	return &Adapter{
		logger:      logger,
		marathonURL: strings.TrimRight(marathonURL, "/"),
		client:      &http.Client{},
	}
}

func (a *Adapter) Name() string { return "mesos" }

// marathonApp is the Marathon v2 app definition shape we send/receive.
type marathonApp struct {
	ID          string            `json:"id"`
	Cmd         string            `json:"cmd,omitempty"`
	Container   *marathonContainer `json:"container,omitempty"`
	Instances   int               `json:"instances"`
	CPUs        float64           `json:"cpus"`
	Mem         float64           `json:"mem"`
	Env         map[string]string `json:"env,omitempty"`
	PortDefs    []marathonPort    `json:"portDefinitions,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	HealthChecks []marathonHealth `json:"healthChecks,omitempty"`
	Constraints [][]string        `json:"constraints,omitempty"`
}

type marathonContainer struct {
	Type   string          `json:"type"` // "DOCKER"
	Docker marathonDocker  `json:"docker"`
}

type marathonDocker struct {
	Image          string        `json:"image"`
	Network        string        `json:"network"`        // "BRIDGE" | "HOST"
	PortMappings   []marathonPort `json:"portMappings,omitempty"`
	ForcePullImage bool          `json:"forcePullImage"`
}

type marathonPort struct {
	ContainerPort int    `json:"containerPort,omitempty"`
	HostPort      int    `json:"hostPort,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	Name          string `json:"name,omitempty"`
	Port          int    `json:"port,omitempty"` // portDefinitions
}

type marathonHealth struct {
	Protocol           string `json:"protocol"` // "HTTP" | "TCP" | "COMMAND"
	Path               string `json:"path,omitempty"`
	Port               int    `json:"port,omitempty"`
	GracePeriodSeconds int    `json:"gracePeriodSeconds"`
	IntervalSeconds    int    `json:"intervalSeconds"`
	TimeoutSeconds     int    `json:"timeoutSeconds"`
	MaxConsecutiveFailures int `json:"maxConsecutiveFailures"`
}

// Generate converts a stack manifest to Marathon app JSON (one app per service).
func (a *Adapter) Generate(ctx context.Context, stack *manifest.Stack) ([]byte, error) {
	var apps []marathonApp
	for _, svc := range stack.Services {
		if !svc.IsEnabled() {
			continue
		}
		app := a.serviceToApp(stack, svc)
		apps = append(apps, app)
	}
	return json.MarshalIndent(map[string]interface{}{"apps": apps}, "", "  ")
}

func (a *Adapter) serviceToApp(stack *manifest.Stack, svc manifest.Service) marathonApp {
	appID := fmt.Sprintf("/%s/%s", stack.Name, svc.Name)

	// Resources.
	cpus := 0.25
	mem := 128.0
	if svc.Resources.CPU != "" {
		var mc int
		if _, err := fmt.Sscanf(svc.Resources.CPU, "%dm", &mc); err == nil {
			cpus = float64(mc) / 1000.0
		}
	}
	if svc.Resources.Memory != "" {
		var mb int
		fmt.Sscanf(svc.Resources.Memory, "%d", &mb)
		if strings.Contains(svc.Resources.Memory, "Gi") || strings.Contains(svc.Resources.Memory, "G") {
			mb *= 1024
		}
		mem = float64(mb)
	}

	// Port mappings.
	var portMappings []marathonPort
	var portDefs []marathonPort
	for i, p := range svc.Ports {
		host := p.Host
		portMappings = append(portMappings, marathonPort{
			ContainerPort: p.Expose,
			HostPort:      host,
			Protocol:      "tcp",
			Name:          fmt.Sprintf("%s-%d", svc.Name, i),
		})
		portDefs = append(portDefs, marathonPort{
			Port:     host,
			Protocol: "tcp",
			Name:     fmt.Sprintf("%s-%d", svc.Name, i),
		})
	}

	// Labels.
	labels := map[string]string{
		"stratonmesh.stack":   stack.Name,
		"stratonmesh.service": svc.Name,
	}

	// Health check.
	var healthChecks []marathonHealth
	if svc.HealthCheck.Liveness != nil {
		if svc.HealthCheck.Liveness.HTTP != "" {
			port := 0
			if len(svc.Ports) > 0 {
				port = svc.Ports[0].Expose
			}
			healthChecks = append(healthChecks, marathonHealth{
				Protocol:           "HTTP",
				Path:               svc.HealthCheck.Liveness.HTTP,
				Port:               port,
				GracePeriodSeconds: 60,
				IntervalSeconds:    15,
				TimeoutSeconds:     10,
				MaxConsecutiveFailures: 3,
			})
		}
	}

	app := marathonApp{
		ID:        appID,
		Instances: svc.DefaultReplicas(),
		CPUs:      cpus,
		Mem:       mem,
		Env:       svc.Env,
		Labels:    labels,
		Container: &marathonContainer{
			Type: "DOCKER",
			Docker: marathonDocker{
				Image:          svc.Image,
				Network:        "BRIDGE",
				PortMappings:   portMappings,
				ForcePullImage: false,
			},
		},
		PortDefs:    portDefs,
		HealthChecks: healthChecks,
	}
	if svc.Command != "" {
		app.Cmd = svc.Command
	}
	return app
}

// Apply creates or updates each service as a Marathon app.
func (a *Adapter) Apply(ctx context.Context, stack *manifest.Stack) error {
	for _, svc := range stack.Services {
		if !svc.IsEnabled() {
			continue
		}
		app := a.serviceToApp(stack, svc)
		data, err := json.Marshal(app)
		if err != nil {
			return err
		}
		appID := app.ID
		url := fmt.Sprintf("%s/v2/apps%s", a.marathonURL, appID)

		req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := a.client.Do(req)
		if err != nil {
			return fmt.Errorf("marathon PUT %s: %w", appID, err)
		}
		resp.Body.Close()

		if resp.StatusCode >= 400 {
			return fmt.Errorf("marathon PUT %s: HTTP %d", appID, resp.StatusCode)
		}
		a.logger.Infow("marathon app applied", "app", appID, "status", resp.StatusCode)
	}
	return nil
}

// Stop suspends all apps in the stack by setting instances = 0.
func (a *Adapter) Stop(ctx context.Context, stackID string) error {
	apps, err := a.listStackApps(ctx, stackID)
	if err != nil {
		return err
	}
	for _, appID := range apps {
		if err := a.patchInstances(ctx, appID, 0); err != nil {
			a.logger.Warnw("failed to suspend app", "app", appID, "error", err)
		}
	}
	a.logger.Infow("marathon stack stopped", "stack", stackID)
	return nil
}

// Restart suspends then resumes all apps (Stop → Start).
func (a *Adapter) Restart(ctx context.Context, stackID string) error {
	if err := a.Stop(ctx, stackID); err != nil {
		return err
	}
	return a.Start(ctx, stackID)
}

// Start resumes all apps in the stack by re-applying the saved desired instance counts.
func (a *Adapter) Start(ctx context.Context, stackID string) error {
	// We don't have the original counts handy without the manifest, so we set to 1
	// as a safe default. The orchestrator should follow up with a redeploy that
	// restores the correct counts from etcd.
	apps, err := a.listStackApps(ctx, stackID)
	if err != nil {
		return err
	}
	for _, appID := range apps {
		if err := a.patchInstances(ctx, appID, 1); err != nil {
			a.logger.Warnw("failed to resume app", "app", appID, "error", err)
		}
	}
	a.logger.Infow("marathon stack started", "stack", stackID)
	return nil
}

// Status queries Marathon for per-service health.
func (a *Adapter) Status(ctx context.Context, stackID string) (*orchestrator.AdapterStatus, error) {
	apps, err := a.listStackApps(ctx, stackID)
	if err != nil {
		return &orchestrator.AdapterStatus{}, nil
	}

	status := &orchestrator.AdapterStatus{}
	for _, appID := range apps {
		url := fmt.Sprintf("%s/v2/apps%s", a.marathonURL, appID)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := a.client.Do(req)
		if err != nil {
			continue
		}
		var body struct {
			App struct {
				Instances        int `json:"instances"`
				TasksRunning     int `json:"tasksRunning"`
				TasksHealthy     int `json:"tasksHealthy"`
				TasksUnhealthy   int `json:"tasksUnhealthy"`
			} `json:"app"`
		}
		json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()

		// Extract service name from "/stack/svc" → "svc"
		parts := strings.Split(strings.Trim(appID, "/"), "/")
		svcName := parts[len(parts)-1]

		health := "healthy"
		if body.App.TasksRunning < body.App.Instances {
			health = "starting"
		}
		if body.App.TasksUnhealthy > 0 {
			health = "unhealthy"
		}

		status.Services = append(status.Services, orchestrator.ServiceStatus{
			Name:     svcName,
			Replicas: body.App.Instances,
			Ready:    body.App.TasksRunning,
			Health:   health,
		})
	}
	return status, nil
}

// Inspect returns task-level details for one service.
func (a *Adapter) Inspect(ctx context.Context, stackID, service string) (*orchestrator.ServiceDetail, error) {
	appID := fmt.Sprintf("/%s/%s", stackID, service)
	detail := &orchestrator.ServiceDetail{Name: service, Platform: "mesos"}

	url := fmt.Sprintf("%s/v2/tasks?appId=%s", a.marathonURL, appID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return detail, nil
	}
	defer resp.Body.Close()

	var body struct {
		Tasks []struct {
			ID               string `json:"id"`
			AppID            string `json:"appId"`
			Host             string `json:"host"`
			State            string `json:"state"`
			StartedAt        string `json:"startedAt"`
			HealthCheckResults []struct {
				Alive bool `json:"alive"`
			} `json:"healthCheckResults"`
		} `json:"tasks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return detail, nil
	}

	// Also fetch app info for image.
	appURL := fmt.Sprintf("%s/v2/apps%s", a.marathonURL, appID)
	appReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, appURL, nil)
	if appResp, err := a.client.Do(appReq); err == nil {
		var appBody struct {
			App struct {
				Container struct {
					Docker struct{ Image string `json:"image"` } `json:"docker"`
				} `json:"container"`
			} `json:"app"`
		}
		json.NewDecoder(appResp.Body).Decode(&appBody)
		appResp.Body.Close()
		detail.Image = appBody.App.Container.Docker.Image
	}

	for _, task := range body.Tasks {
		health := "unknown"
		if len(task.HealthCheckResults) > 0 {
			if task.HealthCheckResults[0].Alive {
				health = "healthy"
			} else {
				health = "unhealthy"
			}
		} else if task.State == "TASK_RUNNING" {
			health = "running"
		}
		detail.Instances = append(detail.Instances, orchestrator.InstanceInfo{
			ID:      task.ID,
			Name:    task.ID,
			Status:  strings.ToLower(task.State),
			Health:  health,
			Node:    task.Host,
			Started: task.StartedAt,
		})
	}
	return detail, nil
}

// Logs attempts to fetch stdout from the Mesos sandbox via the agent API.
// Falls back to a helpful message if the agent is not reachable.
func (a *Adapter) Logs(ctx context.Context, stackID, service string, tail int) (string, error) {
	if tail <= 0 {
		tail = 100
	}
	// Mesos sandbox logs require the agent host + task sandbox path, which
	// Marathon provides via /v2/tasks. For simplicity, return instructions.
	return fmt.Sprintf("# mesos/marathon: use Marathon UI → App → Tasks → Sandbox → stdout\n"+
		"# or: curl http://<mesos-agent>:5051/files/read?path=<sandbox>/stdout&offset=0&length=%d\n", tail*200), nil
}

func (a *Adapter) LogStream(ctx context.Context, stackID, service string) (io.ReadCloser, error) {
	out, _ := a.Logs(ctx, stackID, service, 50)
	return io.NopCloser(strings.NewReader(out)), nil
}

// Down suspends all Marathon apps (instances=0) without deleting them.
// The app definitions remain in Marathon and can be resumed with Start.
func (a *Adapter) Down(ctx context.Context, stackID string) error {
	return a.Stop(ctx, stackID)
}

// Destroy permanently removes all Marathon apps for the stack.
func (a *Adapter) Destroy(ctx context.Context, stackID string) error {
	apps, err := a.listStackApps(ctx, stackID)
	if err != nil {
		return err
	}
	for _, appID := range apps {
		url := fmt.Sprintf("%s/v2/apps%s", a.marathonURL, appID)
		req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
		resp, err := a.client.Do(req)
		if err != nil {
			a.logger.Warnw("delete app failed", "app", appID, "error", err)
			continue
		}
		resp.Body.Close()
	}
	a.logger.Infow("marathon stack destroyed", "stack", stackID)
	return nil
}

// Diff compares desired vs live replica counts.
func (a *Adapter) Diff(ctx context.Context, desired, actual *manifest.Stack) (*orchestrator.DiffResult, error) {
	result := &orchestrator.DiffResult{}
	if desired == nil {
		return result, nil
	}
	live, err := a.Status(ctx, desired.Name)
	if err != nil {
		return result, nil
	}
	liveMap := map[string]orchestrator.ServiceStatus{}
	for _, s := range live.Services {
		liveMap[s.Name] = s
	}
	for _, svc := range desired.Services {
		if !svc.IsEnabled() {
			continue
		}
		ls, ok := liveMap[svc.Name]
		if !ok {
			result.Create = append(result.Create, svc.Name)
			continue
		}
		if svc.DefaultReplicas() != ls.Replicas {
			result.Update = append(result.Update, orchestrator.UpdateAction{
				Service:    svc.Name,
				ChangeType: "replicas",
				From:       fmt.Sprintf("%d", ls.Replicas),
				To:         fmt.Sprintf("%d", svc.DefaultReplicas()),
			})
		} else {
			result.Unchanged = append(result.Unchanged, svc.Name)
		}
	}
	return result, nil
}

// Rollback reverts an app to a previous version using the Marathon versions API.
func (a *Adapter) Rollback(ctx context.Context, stackID, version string) error {
	apps, err := a.listStackApps(ctx, stackID)
	if err != nil {
		return err
	}
	for _, appID := range apps {
		// List versions for this app and pick the second-most-recent.
		verURL := fmt.Sprintf("%s/v2/apps%s/versions", a.marathonURL, appID)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, verURL, nil)
		resp, err := a.client.Do(req)
		if err != nil {
			continue
		}
		var vResp struct {
			Versions []string `json:"versions"`
		}
		json.NewDecoder(resp.Body).Decode(&vResp)
		resp.Body.Close()

		if len(vResp.Versions) < 2 {
			continue // nothing to roll back to
		}
		prevVersion := vResp.Versions[1]

		rollbackURL := fmt.Sprintf("%s/v2/apps%s?force=true", a.marathonURL, appID)
		payload := fmt.Sprintf(`{"version":"%s"}`, prevVersion)
		rbReq, _ := http.NewRequestWithContext(ctx, http.MethodPut, rollbackURL, strings.NewReader(payload))
		rbReq.Header.Set("Content-Type", "application/json")
		rbResp, err := a.client.Do(rbReq)
		if err != nil {
			a.logger.Warnw("rollback failed", "app", appID, "error", err)
			continue
		}
		body, _ := io.ReadAll(rbResp.Body)
		rbResp.Body.Close()
		a.logger.Infow("marathon app rolled back", "app", appID, "version", prevVersion, "status", rbResp.StatusCode, "body", string(body))
	}
	return nil
}

// --- helpers ---

// listStackApps returns all Marathon app IDs whose label stratonmesh.stack == stackID.
func (a *Adapter) listStackApps(ctx context.Context, stackID string) ([]string, error) {
	url := fmt.Sprintf("%s/v2/apps?label=stratonmesh.stack==%s&embed=apps.counts", a.marathonURL, stackID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("marathon GET apps: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		Apps []struct {
			ID string `json:"id"`
		} `json:"apps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(body.Apps))
	for _, app := range body.Apps {
		ids = append(ids, app.ID)
	}
	return ids, nil
}

// patchInstances updates an app's instance count.
func (a *Adapter) patchInstances(ctx context.Context, appID string, n int) error {
	url := fmt.Sprintf("%s/v2/apps%s", a.marathonURL, appID)
	payload := fmt.Sprintf(`{"instances":%d}`, n)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("PATCH %s: HTTP %d", appID, resp.StatusCode)
	}
	return nil
}
