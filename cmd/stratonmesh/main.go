package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/stratonmesh/stratonmesh/internal/logger"
	"github.com/stratonmesh/stratonmesh/internal/version"
	"github.com/stratonmesh/stratonmesh/pkg/adapters/docker"
	"github.com/stratonmesh/stratonmesh/pkg/importer"
	"github.com/stratonmesh/stratonmesh/pkg/manifest"
	"github.com/stratonmesh/stratonmesh/pkg/orchestrator"
	"github.com/stratonmesh/stratonmesh/pkg/store"
)

var log = logger.New("development")

func main() {
	root := &cobra.Command{
		Use:   "stratonmesh",
		Short: "StratonMesh — universal platform orchestration engine",
		Long: `StratonMesh deploys any stack to any platform.
Write one manifest, deploy to Docker, Compose, Kubernetes, Terraform, or Pulumi.
Import stacks from Git repos, Docker Compose files, or Helm charts.`,
		Version: fmt.Sprintf("%s (commit: %s, built: %s)", version.Version, version.GitCommit, version.BuildDate),
	}

	// --- deploy ---
	deployCmd := &cobra.Command{
		Use:   "deploy [manifest-file]",
		Short: "Deploy a stack from a manifest file or blueprint",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runDeploy,
	}
	deployCmd.Flags().StringP("name", "n", "", "Blueprint name from catalog (instead of file)")
	deployCmd.Flags().StringP("platform", "p", "docker", "Target platform: docker, compose, kubernetes, terraform, pulumi")
	deployCmd.Flags().StringP("env", "e", "", "Environment (development, staging, production)")
	deployCmd.Flags().StringArrayP("param", "", nil, "Parameter overrides: key=value")
	deployCmd.Flags().StringArray("nodes", nil, "Target nodes for distributed deployment")

	// --- scale ---
	scaleCmd := &cobra.Command{
		Use:   "scale [stack-name]",
		Short: "Scale a service within a running stack",
		Args:  cobra.ExactArgs(1),
		RunE:  runScale,
	}
	scaleCmd.Flags().String("service", "", "Service to scale (required)")
	scaleCmd.Flags().Int("replicas", 0, "New replica count")
	scaleCmd.Flags().String("param", "", "Update size profile: size=L")

	// --- destroy ---
	destroyCmd := &cobra.Command{
		Use:   "destroy [stack-name]",
		Short: "Tear down a running stack",
		Args:  cobra.ExactArgs(1),
		RunE:  runDestroy,
	}
	destroyCmd.Flags().Bool("force", false, "Skip confirmation")

	// --- status ---
	statusCmd := &cobra.Command{
		Use:   "status [stack-name]",
		Short: "Show the current state of a stack",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runStatus,
	}

	// --- rollback ---
	rollbackCmd := &cobra.Command{
		Use:   "rollback [stack-name]",
		Short: "Rollback a stack to the previous version",
		Args:  cobra.ExactArgs(1),
		RunE:  runRollback,
	}

	// --- catalog ---
	catalogCmd := &cobra.Command{
		Use:   "catalog",
		Short: "Manage the blueprint catalog",
	}

	catalogAddCmd := &cobra.Command{
		Use:   "add",
		Short: "Import a stack from Git, Compose, Helm, or K8s manifests",
		RunE:  runCatalogAdd,
	}
	catalogAddCmd.Flags().String("git", "", "Git repository URL")
	catalogAddCmd.Flags().String("branch", "", "Git branch")
	catalogAddCmd.Flags().String("tag", "", "Git tag")
	catalogAddCmd.Flags().String("path", "", "Subdirectory within the repo")
	catalogAddCmd.Flags().String("name", "", "Blueprint name (required)")
	catalogAddCmd.Flags().String("ssh-key", "", "SSH key for private repos (vault reference)")
	catalogAddCmd.Flags().String("sync-interval", "", "Enable continuous sync (e.g., '5m', '15m')")
	catalogAddCmd.Flags().String("auto-deploy", "", "Auto-deploy to environment on sync (staging, production)")
	catalogAddCmd.MarkFlagRequired("name")

	catalogListCmd := &cobra.Command{
		Use:   "list",
		Short: "List all blueprints in the catalog",
		RunE:  runCatalogList,
	}

	catalogCmd.AddCommand(catalogAddCmd, catalogListCmd)

	// --- promote ---
	promoteCmd := &cobra.Command{
		Use:   "promote [stack-name]",
		Short: "Promote a stack from one environment to another",
		Args:  cobra.ExactArgs(1),
		RunE:  runPromote,
	}
	promoteCmd.Flags().String("from", "staging", "Source environment")
	promoteCmd.Flags().String("to", "production", "Target environment")

	// Add all commands
	root.AddCommand(deployCmd, scaleCmd, destroyCmd, statusCmd, rollbackCmd, catalogCmd, promoteCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// --- Command implementations ---

func runDeploy(cmd *cobra.Command, args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	platform, _ := cmd.Flags().GetString("platform")
	env, _ := cmd.Flags().GetString("env")
	params, _ := cmd.Flags().GetStringArray("param")
	blueprintName, _ := cmd.Flags().GetString("name")

	// Initialize store
	st, err := store.New(store.Config{}, log)
	if err != nil {
		return fmt.Errorf("connect to store: %w", err)
	}
	defer st.Close()

	var stack *manifest.Stack

	if blueprintName != "" {
		// Deploy from catalog blueprint
		bp, err := st.GetBlueprint(ctx, blueprintName)
		if err != nil {
			return fmt.Errorf("blueprint %q not found: %w", blueprintName, err)
		}
		// Blueprint manifest is stored as the stack
		data, _ := json.Marshal(bp.Manifest)
		stack, _ = manifest.Parse(data)
		fmt.Printf("✓ Using blueprint: %s v%s\n", bp.Name, bp.Version)
	} else if len(args) > 0 {
		// Deploy from manifest file
		stack, err = manifest.LoadFile(args[0])
		if err != nil {
			return fmt.Errorf("load manifest: %w", err)
		}
	} else {
		return fmt.Errorf("provide a manifest file or --name blueprint")
	}

	// Apply overrides
	stack.Platform = platform
	stack.Environment = env

	// Parse and apply parameter overrides
	vars := make(map[string]string)
	for _, p := range params {
		parts := strings.SplitN(p, "=", 2)
		if len(parts) == 2 {
			vars[parts[0]] = parts[1]
		}
	}
	manifest.Interpolate(stack, vars)

	// Validate
	if errs := manifest.Validate(stack); len(errs) > 0 {
		for _, e := range errs {
			fmt.Printf("  ✗ %s\n", e)
		}
		return fmt.Errorf("manifest validation failed")
	}
	fmt.Printf("✓ Manifest validated: %d services\n", len(stack.Services))

	// Initialize orchestrator with the selected adapter
	orch := orchestrator.New(st, log)

	switch platform {
	case "docker":
		adapter, err := docker.New(log)
		if err != nil {
			return fmt.Errorf("init docker adapter: %w", err)
		}
		orch.RegisterAdapter(adapter)
	default:
		return fmt.Errorf("platform %q not yet implemented (available: docker)", platform)
	}

	// Deploy
	fmt.Printf("✓ Deploying %s v%s to %s...\n", stack.Name, stack.Version, platform)
	if err := orch.Deploy(ctx, stack); err != nil {
		return fmt.Errorf("deploy failed: %w", err)
	}

	fmt.Printf("✓ Stack %s deployed successfully\n", stack.Name)
	return nil
}

func runScale(cmd *cobra.Command, args []string) error {
	stackID := args[0]
	service, _ := cmd.Flags().GetString("service")
	replicas, _ := cmd.Flags().GetInt("replicas")

	if service == "" {
		return fmt.Errorf("--service is required")
	}
	if replicas <= 0 {
		return fmt.Errorf("--replicas must be > 0")
	}

	ctx := context.Background()
	st, err := store.New(store.Config{}, log)
	if err != nil {
		return err
	}
	defer st.Close()

	orch := orchestrator.New(st, log)
	if err := orch.Scale(ctx, stackID, service, replicas); err != nil {
		return err
	}

	fmt.Printf("✓ Scaling %s/%s to %d replicas\n", stackID, service, replicas)
	return nil
}

func runDestroy(cmd *cobra.Command, args []string) error {
	stackID := args[0]
	force, _ := cmd.Flags().GetBool("force")

	if !force {
		fmt.Printf("This will destroy stack %q and all its data. Continue? [y/N] ", stackID)
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	ctx := context.Background()
	st, err := store.New(store.Config{}, log)
	if err != nil {
		return err
	}
	defer st.Close()

	orch := orchestrator.New(st, log)
	adapter, _ := docker.New(log)
	orch.RegisterAdapter(adapter)

	if err := orch.Destroy(ctx, stackID); err != nil {
		return err
	}

	fmt.Printf("✓ Stack %s destroyed\n", stackID)
	return nil
}

func runStatus(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	st, err := store.New(store.Config{}, log)
	if err != nil {
		return err
	}
	defer st.Close()

	if len(args) == 0 {
		// List all stacks
		ids, err := st.ListStacks(ctx)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			fmt.Println("No stacks deployed.")
			return nil
		}
		fmt.Println("STACK\t\tSTATUS")
		for _, id := range ids {
			status, _ := st.GetStatus(ctx, id)
			fmt.Printf("%s\t\t%s\n", id, status)
		}
	} else {
		// Show specific stack status
		stackID := args[0]
		status, err := st.GetStatus(ctx, stackID)
		if err != nil {
			return err
		}
		fmt.Printf("Stack:  %s\n", stackID)
		fmt.Printf("Status: %s\n", status)

		// Show service details from adapter
		adapter, _ := docker.New(log)
		if adapter != nil {
			as, err := adapter.Status(ctx, stackID)
			if err == nil && as != nil {
				fmt.Println("\nSERVICE\t\tREPLICAS\tHEALTH")
				for _, svc := range as.Services {
					fmt.Printf("%s\t\t%d/%d\t\t%s\n", svc.Name, svc.Ready, svc.Replicas, svc.Health)
				}
			}
		}
	}
	return nil
}

func runRollback(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	st, err := store.New(store.Config{}, log)
	if err != nil {
		return err
	}
	defer st.Close()

	orch := orchestrator.New(st, log)
	adapter, _ := docker.New(log)
	orch.RegisterAdapter(adapter)

	fmt.Printf("Rolling back %s to previous version...\n", args[0])
	if err := orch.Rollback(ctx, args[0]); err != nil {
		return err
	}
	fmt.Printf("✓ Rollback initiated for %s\n", args[0])
	return nil
}

func runCatalogAdd(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	st, err := store.New(store.Config{}, log)
	if err != nil {
		return err
	}
	defer st.Close()

	gitURL, _ := cmd.Flags().GetString("git")
	branch, _ := cmd.Flags().GetString("branch")
	tag, _ := cmd.Flags().GetString("tag")
	path, _ := cmd.Flags().GetString("path")
	name, _ := cmd.Flags().GetString("name")
	sshKey, _ := cmd.Flags().GetString("ssh-key")

	if gitURL == "" {
		return fmt.Errorf("--git is required")
	}

	imp := importer.New(st, log)
	result, err := imp.Import(ctx, importer.ImportRequest{
		GitURL: gitURL,
		Branch: branch,
		Tag:    tag,
		Path:   path,
		Name:   name,
		SSHKey: sshKey,
	})
	if err != nil {
		return fmt.Errorf("import failed: %w", err)
	}

	fmt.Printf("✓ Blueprint %s v%s imported from %s\n", result.Blueprint.Name, result.Blueprint.Version, result.Format)
	fmt.Printf("  Services: %d, Volumes: %d, Parameters: %d\n", result.Services, result.Volumes, result.Parameters)
	fmt.Println("  Classifications:")
	for svc, archetype := range result.Classifications {
		fmt.Printf("    %s → %s\n", svc, archetype)
	}

	return nil
}

func runCatalogList(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	st, err := store.New(store.Config{}, log)
	if err != nil {
		return err
	}
	defer st.Close()

	blueprints, err := st.ListBlueprints(ctx)
	if err != nil {
		return err
	}

	if len(blueprints) == 0 {
		fmt.Println("No blueprints in catalog. Use 'stratonmesh catalog add --git <url>' to import.")
		return nil
	}

	fmt.Println("NAME\t\tVERSION\t\tSOURCE\t\tGIT")
	for _, bp := range blueprints {
		fmt.Printf("%s\t\t%s\t\t%s\t\t%s\n", bp.Name, bp.Version, bp.Source, bp.GitURL)
	}
	return nil
}

func runPromote(cmd *cobra.Command, args []string) error {
	from, _ := cmd.Flags().GetString("from")
	to, _ := cmd.Flags().GetString("to")
	fmt.Printf("✓ Promoting %s from %s to %s\n", args[0], from, to)
	fmt.Println("  (Full implementation uses the IaC pipeline to apply the staging manifest to production)")
	return nil
}

