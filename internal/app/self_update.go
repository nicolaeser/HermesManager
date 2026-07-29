package app

import (
	"context"
	"flag"
	"fmt"

	"github.com/nicolaeser/HermesManager/internal/selfupdate"
	"github.com/nicolaeser/HermesManager/internal/ui"
)

func (app *App) selfUpdateCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("self-update", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	checkOnly := flags.Bool("check", false, "check for a newer release without installing it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return fmt.Errorf("usage: hermes-manager self-update [--check]")
	}

	updater := selfupdate.New(app.Runner, app.In, app.Out, app.Err)
	terminal.Info("Checking GitHub for the latest Hermes Manager release")
	plan, err := updater.Check(ctx, app.Build.Version)
	if err != nil {
		return err
	}

	terminal.Section("Hermes Manager update")
	terminal.KeyValue("Installed", plan.CurrentVersion)
	terminal.KeyValue("Latest", plan.LatestVersion)
	terminal.KeyValue("Executable", plan.TargetPath)
	if !plan.Available {
		terminal.Success("Hermes Manager is already up to date")
		return nil
	}
	if *checkOnly {
		terminal.Warn("A newer Hermes Manager release is available.")
		return nil
	}

	confirmed, err := terminal.Confirm(
		fmt.Sprintf("Download verified release %s and replace the installed Hermes Manager?", plan.LatestVersion),
		false,
	)
	if err != nil {
		return err
	}
	if !confirmed {
		terminal.Info("Self-update cancelled")
		return nil
	}
	terminal.Info("Downloading %s and verifying its SHA-256 checksum", plan.AssetName)
	if err := updater.Apply(ctx, plan); err != nil {
		return err
	}
	terminal.Success("Hermes Manager updated to %s", plan.LatestVersion)
	terminal.Info("The new version is used the next time you run hermes-manager.")
	return nil
}
