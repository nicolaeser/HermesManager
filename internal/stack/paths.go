package stack

import (
	"fmt"
	"path/filepath"
)

type Paths struct {
	Root          string
	Compose       string
	Manager       string
	Config        string
	Secrets       string
	State         string
	OperationsLog string
	Lock          string
	Data          string
	Workspace     string
	Backups       string
}

func NewPaths(root string) (Paths, error) {
	if root == "" {
		return Paths{}, fmt.Errorf("instance folder cannot be empty")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve instance folder: %w", err)
	}
	absolute = filepath.Clean(absolute)
	manager := filepath.Join(absolute, ".manager")
	return Paths{
		Root:          absolute,
		Compose:       filepath.Join(absolute, "compose.yaml"),
		Manager:       manager,
		Config:        filepath.Join(manager, "instance.json"),
		Secrets:       filepath.Join(manager, "secrets.env"),
		State:         filepath.Join(manager, "state.json"),
		OperationsLog: filepath.Join(manager, "operations.log"),
		Lock:          filepath.Join(manager, "operation.lock"),
		// Data is the only critical Hermes persistence root (HERMES_HOME → /opt/data).
		// Official Hermes stores config, sessions, memories, and data/workspace here.
		Data: filepath.Join(absolute, "data"),
		// Workspace is an optional host project directory mounted at /workspace.
		// It is NOT Hermes Agent core state; see data/workspace for that.
		Workspace: filepath.Join(absolute, "workspace"),
		Backups:   filepath.Join(absolute, "backups"),
	}, nil
}

// HermesDataWorkspace is the official Hermes Agent workspace under HERMES_HOME.
func (p Paths) HermesDataWorkspace() string {
	return filepath.Join(p.Data, "workspace")
}

func (p Paths) GetData() string      { return p.Data }
func (p Paths) GetWorkspace() string { return p.Workspace }
func (p Paths) GetBackups() string   { return p.Backups }
