package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type backupCandidate struct {
	Name    string
	ModTime int64
}

func (manager *Manager) BackupsToPrune(keep int) ([]string, error) {
	if keep < 1 {
		return nil, fmt.Errorf("keep must be at least 1")
	}
	if err := manager.RequireInstalled(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(manager.Paths.Backups)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	var candidates []backupCandidate
	for _, entry := range entries {
		if entry.IsDir() || !automaticBackupName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect backup %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		candidates = append(candidates, backupCandidate{
			Name:    entry.Name(),
			ModTime: info.ModTime().UnixNano(),
		})
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].ModTime == candidates[right].ModTime {
			return candidates[left].Name > candidates[right].Name
		}
		return candidates[left].ModTime > candidates[right].ModTime
	})
	if len(candidates) <= keep {
		return nil, nil
	}
	files := make([]string, 0, len(candidates)-keep)
	for _, candidate := range candidates[keep:] {
		files = append(files, candidate.Name)
	}
	return files, nil
}

func (manager *Manager) PruneAutomaticBackups(keep int) (deleted []string, operationErr error) {
	lock, err := manager.operationLock("backup-prune")
	if err != nil {
		return nil, err
	}
	defer releaseLock(lock, &operationErr)

	files, err := manager.BackupsToPrune(keep)
	if err != nil {
		return nil, err
	}
	for _, name := range files {
		if !automaticBackupName(name) || filepath.Base(name) != name {
			return deleted, fmt.Errorf("refuse unsafe backup name %q", name)
		}
		if err := os.Remove(filepath.Join(manager.Paths.Backups, name)); err != nil {
			return deleted, fmt.Errorf("delete backup %s: %w", name, err)
		}
		deleted = append(deleted, name)
	}
	_ = manager.StateStore.Log("backup-prune", fmt.Sprintf("keep=%d deleted=%d result=success", keep, len(deleted)))
	return deleted, nil
}

func automaticBackupName(name string) bool {
	if !strings.HasSuffix(strings.ToLower(name), ".zip") {
		return false
	}
	for _, prefix := range []string{
		"hermes-pre-update-",
		"hermes-pre-restore-",
		"hermes-pre-image-rollback-",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
