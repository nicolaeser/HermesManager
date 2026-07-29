package app

import (
	"context"
	"flag"
	"fmt"

	"github.com/nicolaeser/HermesManager/internal/manager"
	"github.com/nicolaeser/HermesManager/internal/ui"
)

func (app *App) dashboardCLICommand(ctx context.Context, terminal *ui.UI, args []string) error {
	action := "show"
	if len(args) > 0 && args[0] == "reset-password" {
		action = args[0]
		args = args[1:]
	}
	root, err := oneFolder(args)
	if err != nil {
		return fmt.Errorf("usage: hermes-manager dashboard [reset-password] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	if action == "show" {
		return rt.dashboardCommand()
	}
	if err := terminal.RequirePhrase("Resetting the dashboard password signs out every existing dashboard session.", "RESET"); err != nil {
		return err
	}
	password, err := rt.manager.ResetDashboardPassword(ctx)
	if err != nil {
		return err
	}
	access, err := rt.manager.Dashboard()
	if err != nil {
		return err
	}
	terminal.Success("Dashboard password and session secret rotated")
	terminal.KeyValue("URL", access.URL)
	terminal.KeyValue("Username", access.Username)
	terminal.KeyValue("New password", password)
	return nil
}

func (app *App) backupsCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	if len(args) == 0 || args[0] != "prune" {
		root, err := oneFolder(args)
		if err != nil {
			return fmt.Errorf("usage: hermes-manager backups [FOLDER]")
		}
		rt, err := app.runtime(root, terminal)
		if err != nil {
			return err
		}
		return rt.listBackups()
	}
	flags := flag.NewFlagSet("backups prune", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	keep := flags.Int("keep", 10, "number of newest automatic safety backups to retain")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: hermes-manager backups prune [--keep N] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	files, err := rt.manager.BackupsToPrune(*keep)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		terminal.Success("Nothing to prune; manual backups and instance exports are never selected")
		return nil
	}
	terminal.Section("Automatic backups to delete")
	for _, file := range files {
		fmt.Fprintln(app.Out, "  "+file)
	}
	confirmed, err := terminal.Confirm(fmt.Sprintf("Delete %d old automatic safety backup(s)?", len(files)), false)
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("backup pruning cancelled")
	}
	deleted, err := rt.manager.PruneAutomaticBackups(*keep)
	if err != nil {
		return err
	}
	terminal.Success("Deleted %d old automatic backup(s); manual backups were retained", len(deleted))
	return nil
}

func (app *App) exportInstanceCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("export-instance", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	withoutWorkspace := flags.Bool("without-workspace", false, "exclude workspace files")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: hermes-manager export-instance [--without-workspace] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	confirmed, err := terminal.Confirm("Create a disaster-recovery export containing Hermes data and dashboard credentials?", true)
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("instance export cancelled")
	}
	archive, err := rt.manager.ExportInstance(ctx, !*withoutWorkspace)
	if err != nil {
		return err
	}
	terminal.Success("Instance export created and verified: %s", archive)
	terminal.Warn("This archive contains dashboard credentials; store it securely.")
	return nil
}

func (app *App) doctorCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	root, err := oneFolder(args)
	if err != nil {
		return fmt.Errorf("usage: hermes-manager doctor [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	report := rt.manager.Doctor(ctx)
	rt.printDoctor(report)
	if !report.Healthy() {
		return fmt.Errorf("one or more doctor checks failed")
	}
	return nil
}

func (rt runtime) printDoctor(report manager.DoctorReport) {
	rt.ui.Section("Hermes Manager doctor")
	for _, check := range report.Checks {
		fmt.Fprintf(rt.app.Out, "  %-4s  %-24s %s\n", check.Level, check.Name, check.Detail)
	}
}
