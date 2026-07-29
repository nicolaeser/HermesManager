package manager

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/nicolaeser/HermesManager/internal/command"
	"github.com/nicolaeser/HermesManager/internal/config"
	"github.com/nicolaeser/HermesManager/internal/fsutil"
	"github.com/nicolaeser/HermesManager/internal/ports"
	"github.com/nicolaeser/HermesManager/internal/secrets"
)

type InstallOptions struct {
	Name          string
	Image         string
	DashboardPort int
	APIPort       int
	BindAll       bool
	Pull          bool
	Start         bool
}

type InstallResult struct {
	Config  config.Config
	Created bool
}

type Status struct {
	Root          string
	Name          string
	Image         string
	TrackedImage  string
	DashboardURL  string
	DashboardPort int
	APIPort       int
	BindAddress   string
	Data          string
	Workspace     string
	Backups       string
	Containers    string
	Version       string
	DashboardOK   bool
	DashboardInfo string
}

type DashboardAccess struct {
	URL      string
	Username string
	Password string
	Listens  string
}

func (manager *Manager) Install(ctx context.Context, options InstallOptions) (result InstallResult, operationErr error) {
	lock, err := manager.operationLock("install")
	if err != nil {
		return result, err
	}
	defer releaseLock(lock, &operationErr)

	var cfg config.Config
	created := !manager.ConfigStore.Exists()
	if created {
		if fsutil.FileExists(manager.Paths.Compose) {
			return result, fmt.Errorf("%s already exists but is not owned by Hermes Manager", manager.Paths.Compose)
		}
		manager.progress("Creating instance metadata and selecting free ports")
		bindAddress := config.DefaultBindAddress
		if options.BindAll {
			bindAddress = config.PublicBindAddress
		}
		dashboardPort, apiPort, err := ports.Select(manager.Paths.Root, bindAddress, options.DashboardPort, options.APIPort)
		if err != nil {
			return result, err
		}
		cfg = config.New(manager.Paths.Root, options.Name, options.Image, dashboardPort, apiPort)
		cfg.BindAddress = bindAddress
		if err := manager.ConfigStore.Save(cfg); err != nil {
			return result, err
		}
	} else {
		manager.progress("Repairing existing instance (name and ports stay stable)")
		cfg, err = manager.ConfigStore.Load()
		if err != nil {
			return result, err
		}
		if options.Name != "" || options.Image != "" || options.DashboardPort != 0 || options.APIPort != 0 || options.BindAll {
			fmt.Fprintln(manager.Err, "note: install options are ignored for an existing instance; its name and ports remain stable")
		}
		if err := manager.ConfigStore.Save(cfg); err != nil {
			return result, err
		}
	}

	manager.progress("Writing secrets and generating Compose (data/ + optional workspace/ + backups/)")
	values, _, err := manager.SecretStore.LoadOrCreate(cfg.DashboardUsername)
	if err != nil {
		return result, err
	}
	if err := manager.Generator.Prepare(cfg, values); err != nil {
		return result, err
	}
	manager.progress("Checking Docker CLI and Compose file")
	if err := manager.Docker.CheckCLI(ctx); err != nil {
		return result, err
	}
	if err := manager.Docker.ValidateCompose(ctx); err != nil {
		return result, fmt.Errorf("generated Compose configuration is invalid: %w", err)
	}
	if options.Pull || options.Start {
		if err := manager.Docker.CheckDaemon(ctx); err != nil {
			return result, err
		}
	}
	if options.Pull {
		manager.progress("Pulling Hermes image (this can take a while)")
		if err := manager.Docker.Compose(ctx, false, "pull", "hermes"); err != nil {
			return result, fmt.Errorf("pull Hermes image: %w", err)
		}
	}
	if options.Start {
		manager.progress("Starting Hermes container")
		upArgs := []string{"up", "-d"}
		if !options.Pull {
			upArgs = append(upArgs, "--pull", "never")
		}
		upArgs = append(upArgs, "hermes")
		if err := manager.Docker.Compose(ctx, false, upArgs...); err != nil {
			return result, fmt.Errorf("start Hermes: %w", err)
		}
		if err := manager.verifyContainerBinds(ctx); err != nil {
			return result, fmt.Errorf("container started but bind mounts are wrong: %w", err)
		}
	}
	_ = manager.StateStore.Log("install", fmt.Sprintf("created=%t result=success", created))
	return InstallResult{Config: cfg, Created: created}, nil
}

func (manager *Manager) Start(ctx context.Context) (operationErr error) {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	if err := manager.Prepare(ctx, true); err != nil {
		return err
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		return err
	}
	lock, err := manager.operationLock("start")
	if err != nil {
		return err
	}
	defer releaseLock(lock, &operationErr)
	if err := manager.Docker.Compose(ctx, false, "up", "-d", "hermes"); err != nil {
		return err
	}
	_ = manager.StateStore.Log("start", "result=success")
	return nil
}

func (manager *Manager) Stop(ctx context.Context) (operationErr error) {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		return err
	}
	lock, err := manager.operationLock("stop")
	if err != nil {
		return err
	}
	defer releaseLock(lock, &operationErr)
	if err := manager.Docker.Compose(ctx, false, "stop", "hermes"); err != nil {
		return err
	}
	_ = manager.StateStore.Log("stop", "result=success")
	return nil
}

func (manager *Manager) Restart(ctx context.Context) (operationErr error) {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		return err
	}
	lock, err := manager.operationLock("restart")
	if err != nil {
		return err
	}
	defer releaseLock(lock, &operationErr)
	if err := manager.Docker.Compose(ctx, false, "restart", "hermes"); err != nil {
		return err
	}
	_ = manager.StateStore.Log("restart", "result=success")
	return nil
}

func (manager *Manager) Status(ctx context.Context) (Status, error) {
	if err := manager.RequireInstalled(); err != nil {
		return Status{}, err
	}
	cfg, _, err := manager.Load()
	if err != nil {
		return Status{}, err
	}
	status := Status{
		Root:          manager.Paths.Root,
		Name:          cfg.Name,
		Image:         cfg.EffectiveImage(),
		TrackedImage:  cfg.Image,
		DashboardURL:  fmt.Sprintf("http://127.0.0.1:%d", cfg.DashboardPort),
		DashboardPort: cfg.DashboardPort,
		APIPort:       cfg.APIPort,
		BindAddress:   cfg.BindAddress,
		Data:          manager.Paths.Data,
		Workspace:     manager.Paths.Workspace,
		Backups:       manager.Paths.Backups,
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		status.Containers = "Docker unavailable: " + err.Error()
		return status, nil
	}
	status.Containers, err = manager.Docker.ComposeOutput(ctx, "ps")
	if err != nil {
		return Status{}, err
	}
	if manager.Docker.ServiceRunning(ctx) {
		status.Version, _ = manager.Docker.ExecOutput(ctx, "version")
		health, healthErr := manager.DashboardHealth(ctx)
		if healthErr != nil {
			status.DashboardInfo = healthErr.Error()
		} else {
			status.DashboardOK = true
			status.DashboardInfo = fmt.Sprintf("healthy, authentication active, Hermes %s", health.Version)
		}
	}
	return status, nil
}

func (manager *Manager) Dashboard() (DashboardAccess, error) {
	cfg, values, err := manager.Load()
	if err != nil {
		return DashboardAccess{}, err
	}
	return DashboardAccess{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", cfg.DashboardPort),
		Username: values[secrets.DashboardUsername],
		Password: values[secrets.DashboardPassword],
		Listens:  fmt.Sprintf("%s:%d", cfg.BindAddress, cfg.DashboardPort),
	}, nil
}

func (manager *Manager) Logs(ctx context.Context, tail int) error {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	if tail < 1 {
		tail = 100
	}
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := manager.Docker.Compose(runCtx, true, "logs", "-f", "--tail", fmt.Sprintf("%d", tail), "hermes")
	if command.IsInterrupted(err) {
		return nil
	}
	return err
}

func (manager *Manager) IsInstalled() bool {
	return manager.Docker.IsInstalled()
}

func SanitizeLabel(value string) string {
	value = strings.TrimSpace(value)
	var output strings.Builder
	lastDash := false
	for _, character := range value {
		valid := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == '-'
		if valid {
			output.WriteRune(character)
			lastDash = false
		} else if !lastDash {
			output.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(output.String(), "-")
}
