package manager

import (
	"context"
	"fmt"

	"github.com/nicolaeser/HermesManager/internal/secrets"
)

func (manager *Manager) ResetDashboardPassword(ctx context.Context) (password string, operationErr error) {
	if err := manager.RequireInstalled(); err != nil {
		return "", err
	}
	lock, err := manager.operationLock("dashboard-password-reset")
	if err != nil {
		return "", err
	}
	defer releaseLock(lock, &operationErr)
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		return "", err
	}
	_, values, err := manager.Load()
	if err != nil {
		return "", err
	}
	running, err := manager.Docker.ServiceRunningStatus(ctx)
	if err != nil {
		return "", fmt.Errorf("inspect running Hermes container: %w", err)
	}
	exists, err := manager.Docker.ServiceExists(ctx)
	if err != nil {
		return "", fmt.Errorf("inspect existing Hermes container: %w", err)
	}

	previous := cloneSecrets(values)
	password, err = manager.SecretStore.RotateDashboard(values)
	if err != nil {
		return "", err
	}
	if running {
		if err := manager.Docker.Compose(ctx, false, "up", "-d", "--force-recreate", "hermes"); err != nil {
			_ = manager.SecretStore.Save(previous)
			_ = manager.Docker.Compose(ctx, false, "up", "-d", "--force-recreate", "hermes")
			return "", fmt.Errorf("apply new dashboard credentials; previous credentials restored: %w", err)
		}
		if _, err := manager.waitForDashboard(ctx, dashboardReadyTimeout); err != nil {
			_ = manager.SecretStore.Save(previous)
			_ = manager.Docker.Compose(ctx, false, "up", "-d", "--force-recreate", "hermes")
			return "", fmt.Errorf("verify new dashboard credentials; previous credentials restored: %w", err)
		}
	} else if exists {
		if err := manager.Docker.Compose(ctx, false, "rm", "-f", "-s", "hermes"); err != nil {
			_ = manager.SecretStore.Save(previous)
			return "", fmt.Errorf("remove stopped container so new credentials apply; previous credentials restored: %w", err)
		}
	}
	action := "none"
	if running {
		action = "recreated"
	} else if exists {
		action = "removed-stopped-container"
	}
	_ = manager.StateStore.Log("dashboard-password-reset", fmt.Sprintf("container_action=%s sessions_invalidated=true result=success", action))
	return password, nil
}

func cloneSecrets(values secrets.Values) secrets.Values {
	cloned := make(secrets.Values, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
