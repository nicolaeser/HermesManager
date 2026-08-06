package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nicolaeser/HermesManager/internal/command"
	"github.com/nicolaeser/HermesManager/internal/config"
	"github.com/nicolaeser/HermesManager/internal/manager"
	"github.com/nicolaeser/HermesManager/internal/ui"
)

func (rt runtime) withInterrupt(ctx context.Context, fn func(context.Context) error) error {
	opCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := fn(opCtx)
	if command.IsInterrupted(err) {
		return fmt.Errorf("cancelled")
	}
	return err
}

func (rt runtime) mainMenu(ctx context.Context) error {
	if !rt.manager.IsInstalled() {
		rt.ui.Banner(rt.app.Build.Version, rt.paths.Root)
		folder, err := rt.ui.Prompt("Installation folder", rt.paths.Root)
		if err != nil {
			return err
		}
		rebound, err := rt.app.runtime(folder, rt.ui)
		if err != nil {
			return err
		}
		rt = rebound
		installNow, err := rt.ui.Confirm("Install Hermes here?", true)
		if err != nil {
			return err
		}
		if !installNow {
			return nil
		}
		startNow, err := rt.ui.Confirm("Start the container now?", true)
		if err != nil {
			return err
		}
		err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
			result, installErr := rt.manager.Install(opCtx, manager.InstallOptions{
				Pull:  true,
				Start: startNow,
			})
			if installErr != nil {
				return installErr
			}
			if startNow {
				rt.ui.Success("Hermes installed and started")
			} else {
				rt.ui.Success("Hermes installed (not started)")
				rt.ui.Info("Adjust docker-compose.yml or migrate data if needed, then run: hermes-manager start %q", rt.paths.Root)
			}
			rt.printInstallSummary(result.Config)
			return nil
		})
		if err != nil {
			return err
		}
	}

	for {
		rt.ui.Banner(rt.app.Build.Version, rt.paths.Root)
		choice, err := rt.ui.Menu("Operations", []ui.Item{
			{Key: "1", Label: "Start", Description: "Create or start the container"},
			{Key: "2", Label: "Stop", Description: "Keep every persistent file"},
			{Key: "3", Label: "Restart"},
			{Key: "4", Label: "Status", Description: "Show image, ports, paths, and container state"},
			{Key: "5", Label: "Logs", Description: "Follow the latest container output"},
			{Key: "6", Label: "Dashboard access", Description: "Show local URL and generated login"},
			{Key: "7", Label: "Backup", Description: "Create and verify a full Hermes archive"},
			{Key: "8", Label: "Restore", Description: "Make a safety backup, then restore an archive"},
			{Key: "9", Label: "Update", Description: "Backup, pull, recreate, verify, auto-rollback"},
			{Key: "10", Label: "Rollback", Description: "Use the previously recorded image"},
			{Key: "11", Label: "Safety and maintenance", Description: "Doctor, password reset, retention, export, and repair"},
			{Key: "0", Label: "Exit"},
		})
		if err != nil {
			return err
		}

		switch choice {
		case "1":
			err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
				return rt.withSuccess("Hermes started", rt.manager.Start(opCtx))
			})
		case "2":
			err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
				return rt.withSuccess("Hermes stopped; persistent data was retained", rt.manager.Stop(opCtx))
			})
		case "3":
			err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
				return rt.withSuccess("Hermes restarted", rt.manager.Restart(opCtx))
			})
		case "4":
			err = rt.withInterrupt(ctx, rt.statusCommand)
		case "5":
			err = rt.manager.Logs(ctx, 100)
		case "6":
			err = rt.dashboardCommand()
		case "7":
			err = rt.withInterrupt(ctx, rt.menuBackup)
		case "8":
			err = rt.withInterrupt(ctx, rt.menuRestore)
		case "9":
			err = rt.withInterrupt(ctx, rt.menuUpdate)
		case "10":
			err = rt.ui.RequirePhrase("Rollback recreates Hermes with the previous image.", "ROLLBACK")
			if err == nil {
				err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
					return rt.withSuccess("Previous image restored", rt.manager.Rollback(opCtx))
				})
			}
		case "11":
			err = rt.safetyMenu(ctx)
		case "0":
			return nil
		default:
			err = fmt.Errorf("unknown selection %q", choice)
		}
		if err != nil {
			rt.ui.Failure("%v", err)
		}
		if choice != "5" {
			rt.ui.Pause()
		}
	}
}

func (rt runtime) safetyMenu(ctx context.Context) error {
	for {
		rt.ui.Banner(rt.app.Build.Version, rt.paths.Root)
		choice, err := rt.ui.Menu("Safety and maintenance", []ui.Item{
			{Key: "1", Label: "Run doctor", Description: "Validate storage, mounts, Docker, and dashboard health"},
			{Key: "2", Label: "Reset dashboard password", Description: "Rotate credentials and invalidate existing sessions"},
			{Key: "3", Label: "Prune automatic backups", Description: "Manual backups and instance exports are retained"},
			{Key: "4", Label: "Export complete instance", Description: "Hermes data, workspace, credentials, and recovery metadata"},
			{Key: "5", Label: "Repair managed files", Description: "Regenerate Compose without changing data or ports"},
			{Key: "6", Label: "Toggle network bind", Description: "Switch dashboard/API between 127.0.0.1 and 0.0.0.0 (recreates container)"},
			{Key: "7", Label: "Explain storage layout", Description: "data/ vs workspace/ and what updates preserve"},
			{Key: "0", Label: "Back"},
		})
		if err != nil {
			return err
		}
		switch choice {
		case "1":
			err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
				report := rt.manager.Doctor(opCtx)
				rt.printDoctor(report)
				if !report.Healthy() {
					return fmt.Errorf("one or more doctor checks failed")
				}
				return nil
			})
		case "2":
			err = rt.ui.RequirePhrase("Resetting the password signs out every dashboard session.", "RESET")
			if err == nil {
				err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
					password, resetErr := rt.manager.ResetDashboardPassword(opCtx)
					if resetErr != nil {
						return resetErr
					}
					rt.ui.Success("Dashboard password and session secret rotated")
					rt.ui.KeyValue("New password", password)
					return nil
				})
			}
		case "3":
			err = rt.menuPruneBackups()
		case "4":
			err = rt.withInterrupt(ctx, rt.menuExportInstance)
		case "5":
			err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
				if _, repairErr := rt.manager.Install(opCtx, manager.InstallOptions{}); repairErr != nil {
					return repairErr
				}
				rt.ui.Success("Managed files repaired; persistent data was untouched")
				return nil
			})
		case "6":
			err = rt.withInterrupt(ctx, rt.menuToggleBind)
		case "7":
			rt.printStorageLayout()
			err = nil
		case "0":
			return nil
		default:
			err = fmt.Errorf("unknown selection %q", choice)
		}
		if err != nil {
			rt.ui.Failure("%v", err)
		}
		rt.ui.Pause()
	}
}

func (rt runtime) menuPruneBackups() error {
	keep, err := rt.ui.PromptInt("Automatic backups to keep", 10)
	if err != nil {
		return err
	}
	files, err := rt.manager.BackupsToPrune(keep)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		rt.ui.Success("Nothing to prune")
		return nil
	}
	for _, file := range files {
		fmt.Fprintln(rt.app.Out, "  "+file)
	}
	confirmed, err := rt.ui.Confirm(fmt.Sprintf("Delete %d old automatic backup(s)?", len(files)), false)
	if err != nil {
		return err
	}
	if !confirmed {
		return nil
	}
	deleted, err := rt.manager.PruneAutomaticBackups(keep)
	if err != nil {
		return err
	}
	rt.ui.Success("Deleted %d old automatic backup(s); manual backups were retained", len(deleted))
	return nil
}

func (rt runtime) menuExportInstance(ctx context.Context) error {
	includeWorkspace, err := rt.ui.Confirm("Include workspace files in the export?", true)
	if err != nil {
		return err
	}
	confirmed, err := rt.ui.Confirm("The export contains dashboard credentials. Create it now?", true)
	if err != nil {
		return err
	}
	if !confirmed {
		return nil
	}
	archive, err := rt.manager.ExportInstance(ctx, includeWorkspace)
	if err != nil {
		return err
	}
	rt.ui.Success("Instance export created and verified: %s", archive)
	rt.ui.Warn("Store this archive securely because it contains dashboard credentials.")
	return nil
}

func (rt runtime) menuBackup(ctx context.Context) error {
	label, err := rt.ui.Prompt("Backup label", "manual")
	if err != nil {
		return err
	}
	archive, err := rt.manager.Backup(ctx, label)
	if err != nil {
		return err
	}
	rt.ui.Success("Backup created and verified: %s", archive)
	return nil
}

func (rt runtime) menuRestore(ctx context.Context) error {
	if err := rt.listBackups(); err != nil {
		return err
	}
	archive, err := rt.ui.Prompt("Backup filename or absolute path", "")
	if err != nil {
		return err
	}
	if err := rt.ui.RequirePhrase("Restore replaces Hermes configuration and user data. A safety backup is created first.", "RESTORE"); err != nil {
		return err
	}
	if err := rt.manager.Restore(ctx, archive); err != nil {
		return err
	}
	rt.ui.Success("Restore completed")
	return nil
}

func (rt runtime) menuUpdate(ctx context.Context) error {
	rt.ui.Info("Update pulls a new image and recreates the container only.")
	rt.ui.Info("Host folders data/, workspace/, and backups/ stay bind-mounted and are not deleted.")
	confirmed, err := rt.ui.Confirm("Create a backup, pull the newest image, and update Hermes?", true)
	if err != nil {
		return err
	}
	if !confirmed {
		return nil
	}
	return rt.withSuccess("Update completed and verified; host data was preserved", rt.manager.Update(ctx))
}

func (rt runtime) menuToggleBind(ctx context.Context) error {
	cfg, err := rt.manager.ConfigStore.Load()
	if err != nil {
		return err
	}
	currentlyPublic := cfg.BindAddress == config.PublicBindAddress
	rt.ui.Section("Network bind")
	rt.ui.KeyValue("Current", cfg.BindAddress)
	if currentlyPublic {
		rt.ui.Warn("Ports are published on every host interface (0.0.0.0).")
	} else {
		rt.ui.Info("Ports are localhost-only (127.0.0.1). Use an SSH tunnel for remote access.")
	}

	var public bool
	if currentlyPublic {
		confirmed, confErr := rt.ui.Confirm("Switch to localhost-only (127.0.0.1)?", true)
		if confErr != nil {
			return confErr
		}
		if !confirmed {
			return nil
		}
		public = false
	} else {
		if err := rt.ui.RequirePhrase(
			"Publishing on 0.0.0.0 exposes the dashboard and API on every interface. Protect with firewall/VPN/proxy.",
			"BIND-ALL",
		); err != nil {
			return err
		}
		public = true
	}
	if err := rt.manager.SetBindAddress(ctx, public); err != nil {
		return err
	}
	cfg, err = rt.manager.ConfigStore.Load()
	if err != nil {
		return err
	}
	rt.ui.Success("Bind address is now %s", cfg.BindAddress)
	rt.ui.KeyValue("Dashboard", fmt.Sprintf("http://127.0.0.1:%d", cfg.DashboardPort))
	return nil
}

func (rt runtime) printStorageLayout() {
	rt.ui.Section("Storage layout")
	rt.ui.KeyValue("Instance root", rt.paths.Root)
	rt.ui.KeyValue("Hermes data", rt.paths.Data)
	rt.ui.Info("Mounted at /opt/data (HERMES_HOME). Official Hermes stores config, sessions, memories, skills, and data/workspace here. This is the only critical store for image updates.")
	rt.ui.KeyValue("Agent workspace", rt.paths.HermesDataWorkspace())
	rt.ui.Info("Created by Hermes Agent under HERMES_HOME — not the host project folder.")
	rt.ui.KeyValue("Project workspace", rt.paths.Workspace)
	rt.ui.Info("Optional host directory mounted at /workspace for project files. Empty by default; not required by official Hermes Docker.")
	rt.ui.KeyValue("Backups", rt.paths.Backups)
	rt.ui.Info("Host archive directory used by hermes backup/import.")
	rt.ui.Section("What updates preserve")
	rt.ui.Info("Update = backup → pull image → force-recreate container → verify mounts, version, dashboard.")
	rt.ui.Info("Host data/, workspace/, and backups/ are bind mounts and are never removed by update.")
	rt.ui.Info("On failure the previous image is pinned and recreated automatically when possible.")
}

func (rt runtime) printInstallSummary(cfg config.Config) {
	rt.ui.KeyValue("Folder", rt.paths.Root)
	rt.ui.KeyValue("Instance", cfg.Name)
	rt.ui.KeyValue("Dashboard", fmt.Sprintf("http://127.0.0.1:%d", cfg.DashboardPort))
	rt.ui.KeyValue("Listening on", cfg.BindAddress)
	rt.ui.KeyValue("API host port", cfg.APIPort)
	rt.ui.KeyValue("Hermes data", rt.paths.Data+"  (critical HERMES_HOME)")
	rt.ui.KeyValue("Project workspace", rt.paths.Workspace+"  (optional)")
	rt.ui.Info("Hermes Agent state lives under data/. The host workspace/ folder is optional project files.")
	rt.ui.Info("Run 'hermes-manager dashboard %q' to show the generated login.", rt.paths.Root)
}
