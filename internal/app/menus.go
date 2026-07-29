package app

import (
	"context"
	"fmt"

	"github.com/nicolaeser/HermesManager/internal/config"
	"github.com/nicolaeser/HermesManager/internal/manager"
	"github.com/nicolaeser/HermesManager/internal/ui"
)

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
		installNow, err := rt.ui.Confirm("Install and start Hermes here?", true)
		if err != nil {
			return err
		}
		if !installNow {
			return nil
		}
		result, err := rt.manager.Install(ctx, manager.InstallOptions{Pull: true, Start: true})
		if err != nil {
			return err
		}
		rt.ui.Success("Hermes installed")
		rt.printInstallSummary(result.Config)
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
			err = rt.withSuccess("Hermes started", rt.manager.Start(ctx))
		case "2":
			err = rt.withSuccess("Hermes stopped; persistent data was retained", rt.manager.Stop(ctx))
		case "3":
			err = rt.withSuccess("Hermes restarted", rt.manager.Restart(ctx))
		case "4":
			err = rt.statusCommand(ctx)
		case "5":
			err = rt.manager.Logs(ctx, 100)
		case "6":
			err = rt.dashboardCommand()
		case "7":
			err = rt.menuBackup(ctx)
		case "8":
			err = rt.menuRestore(ctx)
		case "9":
			err = rt.menuUpdate(ctx)
		case "10":
			err = rt.ui.RequirePhrase("Rollback recreates Hermes with the previous image.", "ROLLBACK")
			if err == nil {
				err = rt.withSuccess("Previous image restored", rt.manager.Rollback(ctx))
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
			{Key: "0", Label: "Back"},
		})
		if err != nil {
			return err
		}
		switch choice {
		case "1":
			report := rt.manager.Doctor(ctx)
			rt.printDoctor(report)
			if !report.Healthy() {
				err = fmt.Errorf("one or more doctor checks failed")
			}
		case "2":
			err = rt.ui.RequirePhrase("Resetting the password signs out every dashboard session.", "RESET")
			if err == nil {
				var password string
				password, err = rt.manager.ResetDashboardPassword(ctx)
				if err == nil {
					rt.ui.Success("Dashboard password and session secret rotated")
					rt.ui.KeyValue("New password", password)
				}
			}
		case "3":
			err = rt.menuPruneBackups()
		case "4":
			err = rt.menuExportInstance(ctx)
		case "5":
			_, err = rt.manager.Install(ctx, manager.InstallOptions{})
			if err == nil {
				rt.ui.Success("Managed files repaired; persistent data was untouched")
			}
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
	confirmed, err := rt.ui.Confirm("Create a backup, pull the newest image, and update Hermes?", true)
	if err != nil {
		return err
	}
	if !confirmed {
		return nil
	}
	return rt.withSuccess("Update completed and verified", rt.manager.Update(ctx))
}

func (rt runtime) printInstallSummary(cfg config.Config) {
	rt.ui.KeyValue("Folder", rt.paths.Root)
	rt.ui.KeyValue("Instance", cfg.Name)
	rt.ui.KeyValue("Dashboard", fmt.Sprintf("http://127.0.0.1:%d", cfg.DashboardPort))
	rt.ui.KeyValue("Listening on", cfg.BindAddress)
	rt.ui.KeyValue("API host port", cfg.APIPort)
	rt.ui.KeyValue("Persistent data", rt.paths.Data)
	rt.ui.Info("Run 'hermes-manager dashboard %q' to show the generated login.", rt.paths.Root)
}
