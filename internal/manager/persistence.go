package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type mountInfo struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

// assertHostPersistence ensures instance host directories exist. Image updates
// must never delete these bind-mount sources.
func (manager *Manager) assertHostPersistence() error {
	for _, directory := range []struct {
		label string
		path  string
	}{
		{"Hermes data (HERMES_HOME)", manager.Paths.Data},
		{"project workspace", manager.Paths.Workspace},
		{"backups", manager.Paths.Backups},
	} {
		info, err := os.Stat(directory.path)
		if err != nil {
			return fmt.Errorf("%s directory missing (%s): %w", directory.label, directory.path, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s path is not a directory: %s", directory.label, directory.path)
		}
	}
	return nil
}

// assertDataSurvived checks that host Hermes data still looks intact after a
// container recreate (update/rollback). We never remove host files; this is a
// safety net against mis-mounted empty volumes.
func (manager *Manager) assertDataSurvived(preUpdateHadHermesState bool) error {
	if err := manager.assertHostPersistence(); err != nil {
		return err
	}
	if !preUpdateHadHermesState {
		return nil
	}
	if !manager.hermesStatePresent() {
		return fmt.Errorf(
			"Hermes data under %s is missing after container recreate; host bind mount may be wrong — do not delete %s",
			manager.Paths.Data,
			manager.Paths.Data,
		)
	}
	return nil
}

func (manager *Manager) hermesStatePresent() bool {
	// Any of these indicates a previously used HERMES_HOME (/opt/data).
	markers := []string{
		filepath.Join(manager.Paths.Data, "config.yaml"),
		filepath.Join(manager.Paths.Data, ".env"),
		filepath.Join(manager.Paths.Data, "state.db"),
		filepath.Join(manager.Paths.Data, "sessions"),
		filepath.Join(manager.Paths.Data, "SOUL.md"),
	}
	for _, marker := range markers {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return false
}

// verifyContainerBinds checks the running container's mounts match the host
// instance paths. This is the primary guarantee that updates cannot orphan data
// onto a throwaway container filesystem.
func (manager *Manager) verifyContainerBinds(ctx context.Context) error {
	containerID, err := manager.Docker.ComposeOutput(ctx, "ps", "-q", "hermes")
	if err != nil {
		return fmt.Errorf("resolve container for mount check: %w", err)
	}
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return fmt.Errorf("Hermes container is not running; cannot verify bind mounts")
	}
	raw, err := manager.Docker.DockerOutput(ctx, "inspect", "--format", "{{json .Mounts}}", containerID)
	if err != nil {
		return fmt.Errorf("inspect container mounts: %w", err)
	}
	var mounts []mountInfo
	if err := json.Unmarshal([]byte(raw), &mounts); err != nil {
		return fmt.Errorf("parse container mounts: %w", err)
	}

	byTarget := make(map[string]mountInfo, len(mounts))
	for _, mount := range mounts {
		byTarget[mount.Destination] = mount
	}

	expected := map[string]string{
		"/opt/data":  resolvePath(manager.Paths.Data),
		"/workspace": resolvePath(manager.Paths.Workspace),
		"/backups":   resolvePath(manager.Paths.Backups),
	}
	for target, wantSource := range expected {
		mount, ok := byTarget[target]
		if !ok {
			return fmt.Errorf("container is missing required bind mount %s → %s", wantSource, target)
		}
		if !strings.EqualFold(mount.Type, "bind") {
			return fmt.Errorf("mount %s is type %q, expected bind (anonymous volumes would lose data on recreate)", target, mount.Type)
		}
		got := resolvePath(mount.Source)
		if got != wantSource {
			return fmt.Errorf("mount %s points at %s, expected %s", target, got, wantSource)
		}
	}
	return nil
}

func resolvePath(path string) string {
	cleaned := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved
	}
	return cleaned
}

// preUpdateSnapshot records whether Hermes already had durable state so we can
// detect an empty/wrong mount after recreate.
func (manager *Manager) preUpdateSnapshot() (hadState bool, err error) {
	if err := manager.assertHostPersistence(); err != nil {
		return false, err
	}
	return manager.hermesStatePresent(), nil
}
