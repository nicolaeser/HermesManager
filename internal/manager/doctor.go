package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/nicolaeser/HermesManager/internal/config"
	"github.com/nicolaeser/HermesManager/internal/ports"
	"github.com/nicolaeser/HermesManager/internal/secrets"
)

type DashboardHealth struct {
	OK           bool   `json:"ok"`
	Version      string `json:"version"`
	AuthRequired bool   `json:"auth_required"`
}

type CheckLevel string

const (
	CheckPass CheckLevel = "PASS"
	CheckWarn CheckLevel = "WARN"
	CheckFail CheckLevel = "FAIL"
)

type DoctorCheck struct {
	Level  CheckLevel
	Name   string
	Detail string
}

type DoctorReport struct {
	Checks []DoctorCheck
}

const dashboardReadyTimeout = 2 * time.Minute

func (report *DoctorReport) add(level CheckLevel, name, detail string) {
	report.Checks = append(report.Checks, DoctorCheck{Level: level, Name: name, Detail: detail})
}

func (report DoctorReport) Healthy() bool {
	for _, check := range report.Checks {
		if check.Level == CheckFail {
			return false
		}
	}
	return true
}

func (manager *Manager) DashboardHealth(ctx context.Context) (DashboardHealth, error) {
	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		return DashboardHealth{}, err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/api/health", cfg.DashboardPort)
	return requestDashboardHealth(ctx, url)
}

func requestDashboardHealth(ctx context.Context, url string) (DashboardHealth, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return DashboardHealth{}, err
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return DashboardHealth{}, fmt.Errorf("dashboard health request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return DashboardHealth{}, fmt.Errorf("dashboard health returned HTTP %d", response.StatusCode)
	}
	var health DashboardHealth
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err := decoder.Decode(&health); err != nil {
		return DashboardHealth{}, fmt.Errorf("decode dashboard health: %w", err)
	}
	if !health.OK {
		return health, fmt.Errorf("dashboard reported ok=false")
	}
	if !health.AuthRequired {
		return health, fmt.Errorf("dashboard is reachable but authentication is not active")
	}
	return health, nil
}

func (manager *Manager) waitForDashboard(ctx context.Context, timeout time.Duration) (DashboardHealth, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		health, err := manager.DashboardHealth(ctx)
		if err == nil {
			return health, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return DashboardHealth{}, fmt.Errorf("dashboard did not become healthy within %s: %w", timeout, lastErr)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return DashboardHealth{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (manager *Manager) Doctor(ctx context.Context) DoctorReport {
	var report DoctorReport
	if err := manager.RequireInstalled(); err != nil {
		report.add(CheckFail, "Installation", err.Error())
		return report
	}

	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		report.add(CheckFail, "Instance metadata", err.Error())
		return report
	}
	report.add(CheckPass, "Instance metadata", fmt.Sprintf("%s, bind %s, dashboard %d, API %d", cfg.Name, cfg.BindAddress, cfg.DashboardPort, cfg.APIPort))
	if cfg.BindAddress == config.PublicBindAddress {
		report.add(CheckWarn, "Network exposure", "dashboard and API are published on every host interface (0.0.0.0)")
	} else {
		report.add(CheckPass, "Network exposure", "dashboard and API are localhost-only")
	}
	if conflicts := ports.Conflicts(manager.Paths.Root, cfg.BindAddress, cfg.DashboardPort, cfg.APIPort); len(conflicts) > 0 {
		for _, detail := range conflicts {
			report.add(CheckFail, "Port conflict", detail)
		}
	} else {
		report.add(CheckPass, "Port uniqueness", fmt.Sprintf("dashboard %d and API %d are not reserved by sibling Hermes instances", cfg.DashboardPort, cfg.APIPort))
	}

	values, err := manager.SecretStore.Load()
	if err != nil {
		report.add(CheckFail, "Dashboard credentials", err.Error())
	} else if values[secrets.DashboardUsername] == "" ||
		values[secrets.DashboardPassword] == "" ||
		values[secrets.DashboardSecret] == "" {
		report.add(CheckFail, "Dashboard credentials", "one or more required values are empty")
	} else {
		report.add(CheckPass, "Dashboard credentials", "required values are present")
	}

	for _, directory := range []struct {
		name string
		path string
		hint string
	}{
		{"Manager directory", manager.Paths.Manager, ""},
		{"Hermes data (HERMES_HOME)", manager.Paths.Data, "critical: config, sessions, memories; survives image updates"},
		{"Project workspace (host)", manager.Paths.Workspace, "optional /workspace mount; not Hermes core state"},
		{"Backups", manager.Paths.Backups, "host archive directory for hermes backup/import"},
	} {
		name, path := directory.name, directory.path
		info, statErr := os.Stat(path)
		if statErr != nil {
			report.add(CheckFail, name, statErr.Error())
			continue
		}
		if !info.IsDir() {
			report.add(CheckFail, name, path+" is not a directory")
			continue
		}
		if writeErr := probeWritable(path); writeErr != nil {
			report.add(CheckFail, name, "not writable: "+writeErr.Error())
		} else if directory.hint != "" {
			report.add(CheckPass, name, path+" — "+directory.hint)
		} else {
			report.add(CheckPass, name, path)
		}
	}

	// Official Hermes Agent workspace lives under HERMES_HOME after first boot.
	agentWorkspace := manager.Paths.HermesDataWorkspace()
	if info, err := os.Stat(agentWorkspace); err == nil && info.IsDir() {
		report.add(CheckPass, "Hermes agent workspace", agentWorkspace+" — created by Hermes under data/ (official layout)")
	} else {
		report.add(CheckWarn, "Hermes agent workspace", agentWorkspace+" not present yet (created on first Hermes boot)")
	}

	checkPrivateMode(&report, "Manager permissions", manager.Paths.Manager, 0o700)
	checkPrivateMode(&report, "Metadata permissions", manager.Paths.Config, 0o600)
	checkPrivateMode(&report, "Credential permissions", manager.Paths.Secrets, 0o600)

	composePath := manager.Paths.Compose
	if _, err := os.Stat(composePath); err != nil {
		composePath = manager.Paths.LegacyCompose()
	}
	composeContent, err := os.ReadFile(composePath)
	if err != nil {
		report.add(CheckFail, "Compose mount policy", err.Error())
	} else {
		composeText := string(composeContent)
		required := []string{"./data:/opt/data", "./workspace:/workspace", "./backups:/backups"}
		safe := true
		for _, value := range required {
			safe = safe && strings.Count(composeText, value) == 1
		}
		for _, forbidden := range []string{"/opt/hermes", "docker.sock"} {
			safe = safe && !strings.Contains(composeText, forbidden)
		}
		if safe {
			report.add(CheckPass, "Compose mount policy", "bind mounts only: ./data:/opt/data, ./workspace:/workspace, ./backups:/backups")
		} else {
			report.add(CheckFail, "Compose mount policy", "generated file does not match the three-mount safety contract; run install to repair")
		}
	}

	if manager.hermesStatePresent() {
		report.add(CheckPass, "Hermes state on host", "durable files found under data/ (update will preserve them)")
	} else {
		report.add(CheckWarn, "Hermes state on host", "no config/sessions yet under data/; empty until first successful start")
	}

	if available, diskErr := availableBytes(manager.Paths.Root); diskErr != nil {
		report.add(CheckWarn, "Free disk space", diskErr.Error())
	} else {
		level := CheckPass
		if available < 5<<30 {
			level = CheckWarn
		}
		report.add(level, "Free disk space", humanBytes(available)+" available")
	}

	backups, err := manager.ListBackups()
	if err != nil {
		report.add(CheckWarn, "Backups", err.Error())
	} else if len(backups) == 0 {
		report.add(CheckWarn, "Backups", "no Hermes backup exists yet")
	} else {
		report.add(CheckPass, "Backups", fmt.Sprintf("%d archive(s)", len(backups)))
	}

	if err := manager.Docker.CheckCLI(ctx); err != nil {
		report.add(CheckFail, "Docker CLI and Compose", err.Error())
		return report
	}
	report.add(CheckPass, "Docker CLI and Compose", "available")
	if err := manager.Docker.ValidateCompose(ctx); err != nil {
		report.add(CheckFail, "Compose validation", err.Error())
	} else {
		report.add(CheckPass, "Compose validation", "valid")
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		report.add(CheckFail, "Docker daemon", err.Error())
		return report
	}
	report.add(CheckPass, "Docker daemon", "available")
	if !manager.Docker.ServiceRunning(ctx) {
		report.add(CheckWarn, "Hermes container", "not running")
		for _, item := range []struct {
			port int
			role string
		}{
			{cfg.DashboardPort, "dashboard"},
			{cfg.APIPort, "API"},
		} {
			if !ports.Available(cfg.BindAddress, item.port) {
				report.add(CheckWarn, "Port availability", fmt.Sprintf("%s port %d is already in use on %s; start may fail", item.role, item.port, cfg.BindAddress))
			}
		}
		return report
	}
	report.add(CheckPass, "Hermes container", "running")

	if err := manager.verifyContainerBinds(ctx); err != nil {
		report.add(CheckFail, "Live bind mounts", err.Error())
	} else {
		report.add(CheckPass, "Live bind mounts", "container /opt/data, /workspace, /backups match host instance paths")
	}

	health, err := manager.DashboardHealth(ctx)
	if err != nil {
		report.add(CheckFail, "Dashboard health", err.Error())
	} else {
		report.add(CheckPass, "Dashboard health", fmt.Sprintf("healthy, Hermes %s, authentication active", health.Version))
	}
	return report
}

func probeWritable(directory string) error {
	file, err := os.CreateTemp(directory, ".hermes-manager-doctor-*")
	if err != nil {
		return err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return os.Remove(path)
}

func checkPrivateMode(report *DoctorReport, name, path string, expected os.FileMode) {
	info, err := os.Stat(path)
	if err != nil {
		report.add(CheckFail, name, err.Error())
		return
	}
	actual := info.Mode().Perm()
	if actual != expected {
		report.add(CheckWarn, name, fmt.Sprintf("%s has mode %04o; expected %04o", path, actual, expected))
		return
	}
	report.add(CheckPass, name, fmt.Sprintf("mode %04o", actual))
}

func availableBytes(path string) (uint64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return 0, err
	}
	return uint64(stats.Bavail) * uint64(stats.Bsize), nil
}

func humanBytes(value uint64) string {
	const (
		gib = uint64(1 << 30)
		mib = uint64(1 << 20)
	)
	if value >= gib {
		return fmt.Sprintf("%.1f GiB", float64(value)/float64(gib))
	}
	return fmt.Sprintf("%.1f MiB", float64(value)/float64(mib))
}
