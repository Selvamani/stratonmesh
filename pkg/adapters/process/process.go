package process

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/selvamani/stratonmesh/pkg/manifest"
	"github.com/selvamani/stratonmesh/pkg/orchestrator"
	"go.uber.org/zap"
)

// Adapter implements PlatformAdapter for bare-metal process/systemd management.
type Adapter struct {
	logger *zap.SugaredLogger
}

var _ orchestrator.PlatformAdapter = (*Adapter)(nil)

func New(logger *zap.SugaredLogger) *Adapter { return &Adapter{logger: logger} }
func (a *Adapter) Name() string              { return "process" }

func (a *Adapter) Generate(ctx context.Context, stack *manifest.Stack) ([]byte, error) {
	var out strings.Builder
	for _, svc := range stack.Services {
		if !svc.IsEnabled() {
			continue
		}
		unitName := fmt.Sprintf("sm-%s-%s", stack.Name, svc.Name)
		out.WriteString(fmt.Sprintf("# /etc/systemd/system/%s.service\n", unitName))
		out.WriteString("[Unit]\n")
		out.WriteString(fmt.Sprintf("Description=StratonMesh %s/%s\n", stack.Name, svc.Name))
		out.WriteString("After=network.target\n\n")
		out.WriteString("[Service]\n")
		out.WriteString("Type=simple\n")
		out.WriteString(fmt.Sprintf("ExecStart=%s\n", svc.Command))
		out.WriteString("Restart=always\nRestartSec=5\n")
		for k, v := range svc.Env {
			out.WriteString(fmt.Sprintf("Environment=%s=%s\n", k, v))
		}
		out.WriteString("\n[Install]\nWantedBy=multi-user.target\n\n")
	}
	return []byte(out.String()), nil
}

func (a *Adapter) Apply(ctx context.Context, stack *manifest.Stack) error {
	for _, svc := range stack.Services {
		if !svc.IsEnabled() || svc.Command == "" {
			continue
		}
		a.logger.Infow("starting process", "service", svc.Name, "command", svc.Command)
		parts := strings.Fields(svc.Command)
		cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
		for k, v := range svc.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start %s: %w", svc.Name, err)
		}
	}
	return nil
}

func (a *Adapter) Status(ctx context.Context, id string) (*orchestrator.AdapterStatus, error) {
	return &orchestrator.AdapterStatus{}, nil
}
func (a *Adapter) Destroy(ctx context.Context, id string) error { return nil }
func (a *Adapter) Diff(ctx context.Context, d, act *manifest.Stack) (*orchestrator.DiffResult, error) {
	return &orchestrator.DiffResult{}, nil
}
func (a *Adapter) Stop(ctx context.Context, id string) error    { return nil }
func (a *Adapter) Start(ctx context.Context, id string) error   { return nil }
func (a *Adapter) Restart(ctx context.Context, id string) error { return nil }
func (a *Adapter) Down(ctx context.Context, id string) error    { return nil }
func (a *Adapter) Rollback(ctx context.Context, id, v string) error { return nil }
func (a *Adapter) Inspect(ctx context.Context, stackID, service string) (*orchestrator.ServiceDetail, error) {
	return &orchestrator.ServiceDetail{Name: service, Platform: "process"}, nil
}
func (a *Adapter) Logs(ctx context.Context, stackID, service string, tail int) (string, error) {
	return "# process: systemd journal — use journalctl -u sm-" + stackID + "-" + service, nil
}
func (a *Adapter) LogStream(_ context.Context, stackID, service string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("# process: use journalctl -u sm-" + stackID + "-" + service + " -f\n")), nil
}
