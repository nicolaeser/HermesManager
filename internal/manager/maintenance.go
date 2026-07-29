package manager

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nicolaeser/HermesManager/internal/config"
	"github.com/nicolaeser/HermesManager/internal/fsutil"
)

func (manager *Manager) Backup(ctx context.Context, label string) (archive string, operationErr error) {
	if err := manager.ensureRunning(ctx); err != nil {
		return "", err
	}
	lock, err := manager.operationLock("backup")
	if err != nil {
		return "", err
	}
	defer releaseLock(lock, &operationErr)
	return manager.backupUnlocked(ctx, label)
}

func (manager *Manager) backupUnlocked(ctx context.Context, label string) (string, error) {
	label = SanitizeLabel(label)
	if label == "" {
		label = "manual"
	}
	filename := fmt.Sprintf("hermes-%s-%s.zip", label, time.Now().UTC().Format("20060102T150405.000Z"))
	if err := manager.Docker.Exec(ctx, false, "backup", "-o", "/backups/"+filename); err != nil {
		return "", fmt.Errorf("create Hermes backup: %w", err)
	}
	hostPath := filepath.Join(manager.Paths.Backups, filename)
	if err := os.Chmod(hostPath, 0o600); err != nil {
		return "", fmt.Errorf("protect new backup: %w", err)
	}
	if err := validateZip(hostPath); err != nil {
		return "", fmt.Errorf("validate new backup: %w", err)
	}
	managerState, err := manager.StateStore.Load()
	if err != nil {
		return "", err
	}
	managerState.LastBackup = filename
	managerState.LastOperation = "backup"
	if err := manager.StateStore.Save(managerState); err != nil {
		return "", err
	}
	_ = manager.StateStore.Log("backup", "file="+filename+" result=success")
	return hostPath, nil
}

func (manager *Manager) ListBackups() ([]string, error) {
	if err := manager.RequireInstalled(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(manager.Paths.Backups)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list backups: %w", err)
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".zip") || strings.HasSuffix(entry.Name(), ".tar.gz") {
			files = append(files, entry.Name())
		}
	}
	return files, nil
}

func (manager *Manager) Restore(ctx context.Context, requested string) (operationErr error) {
	if err := manager.ensureRunning(ctx); err != nil {
		return err
	}
	backupsPath := manager.Paths.Backups
	archivePath, archiveName, err := stageRestoreArchive(requested, backupsPath)
	if err != nil {
		return err
	}
	if err := validateZip(archivePath); err != nil {
		return err
	}

	lock, err := manager.operationLock("restore")
	if err != nil {
		return err
	}
	defer releaseLock(lock, &operationErr)
	if _, err := manager.backupUnlocked(ctx, "pre-restore"); err != nil {
		return fmt.Errorf("create pre-restore safety backup: %w", err)
	}
	importErr := manager.Docker.Exec(ctx, false, "import", "/backups/"+archiveName, "--force")
	restartErr := manager.Docker.Compose(ctx, false, "restart", "hermes")
	if importErr != nil {
		return fmt.Errorf("restore backup (container restart attempted): %w", importErr)
	}
	if restartErr != nil {
		return fmt.Errorf("restart after restore: %w", restartErr)
	}
	managerState, err := manager.StateStore.Load()
	if err == nil {
		managerState.LastOperation = "restore"
		_ = manager.StateStore.Save(managerState)
	}
	_ = manager.StateStore.Log("restore", "file="+archiveName+" result=success")
	return nil
}

func (manager *Manager) Update(ctx context.Context) (operationErr error) {
	if err := manager.ensureRunning(ctx); err != nil {
		return err
	}
	lock, err := manager.operationLock("update")
	if err != nil {
		return err
	}
	defer releaseLock(lock, &operationErr)

	// Updates only recreate the container image. Host bind sources under the
	// instance folder (especially data/) are never removed by this manager.
	manager.progress("Checking host data directories and container bind mounts")
	hadState, err := manager.preUpdateSnapshot()
	if err != nil {
		return err
	}
	if err := manager.verifyContainerBinds(ctx); err != nil {
		return fmt.Errorf("pre-update mount check failed (refusing to update with unsafe mounts): %w", err)
	}

	manager.progress("Creating pre-update safety backup")
	if _, err := manager.backupUnlocked(ctx, "pre-update"); err != nil {
		return fmt.Errorf("create pre-update backup: %w", err)
	}
	previousImage, err := manager.currentImageReference(ctx)
	if err != nil {
		return err
	}
	manager.progress("Recording previous image for automatic rollback: %s", previousImage)
	managerState, err := manager.StateStore.Load()
	if err != nil {
		return err
	}
	managerState.PreviousImage = previousImage
	managerState.LastOperation = "update-started"
	if err := manager.StateStore.Save(managerState); err != nil {
		return err
	}
	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		return err
	}
	cfg.PinnedImage = ""
	if err := manager.saveAndPrepare(cfg); err != nil {
		return err
	}
	manager.progress("Pulling the newest Hermes image (host data is not touched)")
	if err := manager.Docker.Compose(ctx, false, "pull", "hermes"); err != nil {
		rollbackErr := manager.pinAndRecreate(ctx, previousImage)
		if rollbackErr != nil {
			return fmt.Errorf("pull updated image: %w; restoring the previous image also failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("pull updated image: %w; the previous image was restored", err)
	}
	// force-recreate replaces the container only. Bind mounts keep host data/.
	// Never use "down -v" or remove host directories here.
	manager.progress("Recreating the container with the new image (preserving bind mounts)")
	if err := manager.Docker.Compose(ctx, false, "up", "-d", "--force-recreate", "--remove-orphans", "hermes"); err != nil {
		rollbackErr := manager.pinAndRecreate(ctx, previousImage)
		return joinedUpdateError("recreate updated container", err, rollbackErr)
	}
	if !manager.Docker.ServiceRunning(ctx) {
		rollbackErr := manager.pinAndRecreate(ctx, previousImage)
		return joinedUpdateError("updated container is not running", nil, rollbackErr)
	}
	manager.progress("Verifying bind mounts and host Hermes data after recreate")
	if err := manager.verifyContainerBinds(ctx); err != nil {
		rollbackErr := manager.pinAndRecreate(ctx, previousImage)
		return joinedUpdateError("post-update mount check failed", err, rollbackErr)
	}
	if err := manager.assertDataSurvived(hadState); err != nil {
		rollbackErr := manager.pinAndRecreate(ctx, previousImage)
		return joinedUpdateError("post-update data check failed", err, rollbackErr)
	}
	manager.progress("Checking Hermes version inside the new container")
	if _, err := manager.Docker.ExecOutput(ctx, "version"); err != nil {
		rollbackErr := manager.pinAndRecreate(ctx, previousImage)
		return joinedUpdateError("updated Hermes version check failed", err, rollbackErr)
	}
	manager.progress("Waiting for dashboard health")
	if _, err := manager.waitForDashboard(ctx, dashboardReadyTimeout); err != nil {
		rollbackErr := manager.pinAndRecreate(ctx, previousImage)
		return joinedUpdateError("updated dashboard health check failed", err, rollbackErr)
	}
	managerState.LastOperation = "update"
	if err := manager.StateStore.Save(managerState); err != nil {
		return err
	}
	_ = manager.StateStore.Log("update", "previous_image="+previousImage+" result=success")
	manager.progress("Update finished; host data/, workspace/, and backups/ were left in place")
	return nil
}

func (manager *Manager) Rollback(ctx context.Context) (operationErr error) {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		return err
	}
	managerState, err := manager.StateStore.Load()
	if err != nil {
		return err
	}
	if managerState.PreviousImage == "" {
		return fmt.Errorf("no previous image has been recorded")
	}
	lock, err := manager.operationLock("rollback")
	if err != nil {
		return err
	}
	defer releaseLock(lock, &operationErr)
	if manager.Docker.ServiceRunning(ctx) {
		if _, err := manager.backupUnlocked(ctx, "pre-image-rollback"); err != nil {
			return fmt.Errorf("create pre-rollback backup: %w", err)
		}
	} else {
		fmt.Fprintln(manager.Err, "warning: current container is not running; proceeding without a pre-rollback backup")
	}
	if err := manager.pinAndRecreate(ctx, managerState.PreviousImage); err != nil {
		return err
	}
	managerState.LastOperation = "rollback"
	if err := manager.StateStore.Save(managerState); err != nil {
		return err
	}
	_ = manager.StateStore.Log("rollback", "image="+managerState.PreviousImage+" result=success")
	return nil
}

func (manager *Manager) currentImageReference(ctx context.Context) (string, error) {
	containerID, err := manager.Docker.ComposeOutput(ctx, "ps", "-q", "hermes")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(containerID) == "" {
		return "", fmt.Errorf("Hermes container is not running")
	}
	imageID, err := manager.Docker.DockerOutput(ctx, "inspect", "--format", "{{.Image}}", strings.TrimSpace(containerID))
	if err != nil {
		return "", err
	}
	reference, err := manager.Docker.DockerOutput(ctx, "image", "inspect", "--format", "{{if .RepoDigests}}{{index .RepoDigests 0}}{{else}}{{.Id}}{{end}}", strings.TrimSpace(imageID))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(reference) == "" {
		return "", fmt.Errorf("could not resolve running image digest")
	}
	return strings.TrimSpace(reference), nil
}

func (manager *Manager) pinAndRecreate(ctx context.Context, image string) error {
	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		return err
	}
	previousPin := cfg.PinnedImage
	cfg.PinnedImage = image
	if err := manager.saveAndPrepare(cfg); err != nil {
		return err
	}
	// Recreate container only — never remove volumes or host directories.
	if err := manager.Docker.Compose(ctx, false, "up", "-d", "--force-recreate", "hermes"); err != nil {
		cfg.PinnedImage = previousPin
		_ = manager.saveAndPrepare(cfg)
		return fmt.Errorf("recreate rollback image: %w", err)
	}
	if err := manager.verifyContainerBinds(ctx); err != nil {
		return fmt.Errorf("rollback image started but bind mounts are wrong: %w", err)
	}
	if _, err := manager.waitForDashboard(ctx, dashboardReadyTimeout); err != nil {
		return fmt.Errorf("rollback image started but dashboard health verification failed: %w", err)
	}
	return nil
}

// SetBindAddress switches published ports between localhost (127.0.0.1) and
// all interfaces (0.0.0.0), regenerates Compose, and recreates the container
// when it is running. Host data is never removed.
func (manager *Manager) SetBindAddress(ctx context.Context, public bool) (operationErr error) {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		return err
	}
	lock, err := manager.operationLock("set-bind-address")
	if err != nil {
		return err
	}
	defer releaseLock(lock, &operationErr)

	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		return err
	}
	want := config.DefaultBindAddress
	if public {
		want = config.PublicBindAddress
	}
	if cfg.BindAddress == want {
		manager.progress("Bind address already %s", want)
		return nil
	}

	manager.progress("Updating bind address %s → %s", cfg.BindAddress, want)
	cfg.BindAddress = want
	if err := manager.saveAndPrepare(cfg); err != nil {
		return err
	}

	running, err := manager.Docker.ServiceRunningStatus(ctx)
	if err != nil {
		return err
	}
	if !running {
		manager.progress("Compose updated; container is stopped — start it to apply the new ports")
		_ = manager.StateStore.Log("set-bind-address", "bind="+want+" running=false result=success")
		return nil
	}

	manager.progress("Recreating container so port publish changes take effect")
	if err := manager.Docker.Compose(ctx, false, "up", "-d", "--force-recreate", "hermes"); err != nil {
		return fmt.Errorf("recreate with new bind address: %w", err)
	}
	if err := manager.verifyContainerBinds(ctx); err != nil {
		return fmt.Errorf("bind address applied but mount check failed: %w", err)
	}
	if _, err := manager.waitForDashboard(ctx, dashboardReadyTimeout); err != nil {
		return fmt.Errorf("bind address applied but dashboard health check failed: %w", err)
	}
	_ = manager.StateStore.Log("set-bind-address", "bind="+want+" result=success")
	return nil
}

func validateZip(path string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("open ZIP %s: %w", path, err)
	}
	defer reader.Close()
	if len(reader.File) == 0 {
		return fmt.Errorf("ZIP archive %s is empty", path)
	}
	for _, file := range reader.File {
		stream, err := file.Open()
		if err != nil {
			return fmt.Errorf("open ZIP member %s: %w", file.Name, err)
		}
		if _, err := io.Copy(io.Discard, stream); err != nil {
			_ = stream.Close()
			return fmt.Errorf("verify ZIP member %s: %w", file.Name, err)
		}
		if err := stream.Close(); err != nil {
			return fmt.Errorf("close ZIP member %s: %w", file.Name, err)
		}
	}
	return nil
}

func stageRestoreArchive(requested, backupsPath string) (string, string, error) {
	if strings.TrimSpace(requested) == "" {
		return "", "", fmt.Errorf("backup path is required")
	}
	if !filepath.IsAbs(requested) {
		requested = filepath.Join(backupsPath, requested)
	}
	absolute, err := filepath.Abs(requested)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", "", fmt.Errorf("inspect backup: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("backup is not a regular file")
	}
	if !strings.HasSuffix(strings.ToLower(absolute), ".zip") {
		return "", "", fmt.Errorf("full restore requires a Hermes .zip backup")
	}
	name := filepath.Base(absolute)
	destination := filepath.Join(backupsPath, name)
	sourceDirectory := filepath.Clean(filepath.Dir(absolute))
	if sourceDirectory != filepath.Clean(backupsPath) {
		if _, err := os.Stat(destination); err == nil {
			return "", "", fmt.Errorf("backup %s already exists in %s", name, backupsPath)
		} else if !os.IsNotExist(err) {
			return "", "", err
		}
		if err := fsutil.CopyFileAtomic(absolute, destination, 0o600); err != nil {
			return "", "", err
		}
	}
	return destination, name, nil
}

func joinedUpdateError(stage string, updateErr, rollbackErr error) error {
	message := stage
	if updateErr != nil {
		message += ": " + updateErr.Error()
	}
	if rollbackErr == nil {
		return fmt.Errorf("%s; automatically rolled back and pinned the previous image", message)
	}
	return fmt.Errorf("%s; automatic rollback also failed: %v", message, rollbackErr)
}
