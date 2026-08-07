package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/nicolaeser/HermesManager/internal/command"
	"github.com/nicolaeser/HermesManager/internal/manager"
	"github.com/nicolaeser/HermesManager/internal/stack"
	"github.com/nicolaeser/HermesManager/internal/ui"
)

type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

type App struct {
	Build  BuildInfo
	In     io.Reader
	Out    io.Writer
	Err    io.Writer
	Runner command.Runner
}

type runtime struct {
	app     *App
	paths   stack.Paths
	manager *manager.Manager
	ui      *ui.UI
}

func New(build BuildInfo) *App {
	return &App{
		Build:  build,
		In:     os.Stdin,
		Out:    os.Stdout,
		Err:    os.Stderr,
		Runner: command.OSRunner{},
	}
}

func (app *App) Run(ctx context.Context, args []string) error {
	global := flag.NewFlagSet("hermes-manager", flag.ContinueOnError)
	global.SetOutput(app.Err)
	assumeYes := global.Bool("yes", false, "confirm guarded operations")
	noColor := global.Bool("no-color", false, "disable ANSI styling")
	if err := global.Parse(args); err != nil {
		return err
	}
	remaining := global.Args()
	commandName := "menu"
	if len(remaining) > 0 {
		commandName = remaining[0]
		remaining = remaining[1:]
	}

	color := false
	if output, ok := app.Out.(*os.File); ok {
		color = ui.ColorEnabled(output, *noColor)
	}
	terminal := ui.New(app.In, app.Out, app.Err, color, *assumeYes)
	switch commandName {
	case "menu", "help", "--help", "-h", "version", "shell":
		return app.dispatch(ctx, terminal, commandName, remaining)
	default:
		cmdCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		err := app.dispatch(cmdCtx, terminal, commandName, remaining)
		if command.IsInterrupted(err) {
			return nil
		}
		return err
	}
}

func (app *App) runtime(root string, terminal *ui.UI) (runtime, error) {
	paths, err := stack.NewPaths(root)
	if err != nil {
		return runtime{}, err
	}
	mgr := manager.New(paths, app.Runner, app.In, app.Out, app.Err)
	mgr.Progress = terminal.Step
	return runtime{
		app:     app,
		paths:   paths,
		manager: mgr,
		ui:      terminal,
	}, nil
}

func (app *App) dispatch(ctx context.Context, terminal *ui.UI, commandName string, args []string) error {
	switch commandName {
	case "help", "--help", "-h":
		app.printHelp()
		return nil
	case "version":
		fmt.Fprintf(app.Out, "Hermes Manager %s\ncommit: %s\nbuilt: %s\n", app.Build.Version, app.Build.Commit, app.Build.Date)
		return nil
	case "self-update":
		return app.selfUpdateCommand(ctx, terminal, args)
	case "install":
		return app.installCommand(ctx, terminal, args)
	case "menu":
		root, err := oneFolder(args)
		if err != nil {
			return fmt.Errorf("usage: hermes-manager menu [FOLDER]")
		}
		rt, err := app.runtime(root, terminal)
		if err != nil {
			return err
		}
		return rt.mainMenu(ctx)
	case "start":
		return app.startCommand(ctx, terminal, args)
	case "stop", "restart", "status", "update", "rollback":
		root, err := oneFolder(args)
		if err != nil {
			return fmt.Errorf("usage: hermes-manager %s [FOLDER]", commandName)
		}
		rt, err := app.runtime(root, terminal)
		if err != nil {
			return err
		}
		return rt.simpleCommand(ctx, commandName)
	case "dashboard":
		return app.dashboardCLICommand(ctx, terminal, args)
	case "backups":
		return app.backupsCommand(ctx, terminal, args)
	case "doctor":
		return app.doctorCommand(ctx, terminal, args)
	case "export-instance":
		return app.exportInstanceCommand(ctx, terminal, args)
	case "logs":
		return app.logsCommand(ctx, terminal, args)
	case "shell":
		return app.shellCommand(ctx, terminal, args)
	case "backup":
		return app.backupCommand(ctx, terminal, args)
	case "restore":
		return app.restoreCommand(ctx, terminal, args)
	default:
		return fmt.Errorf("unknown command %q; run hermes-manager help", commandName)
	}
}

func (app *App) installCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	name := flags.String("name", "", "container/project name (default: derived from folder)")
	image := flags.String("image", "", "Hermes image (default: "+configDefaultImage()+")")
	dashboardPort := flags.Int("dashboard-port", 0, "dashboard host port (default: automatic)")
	apiPort := flags.Int("api-port", 0, "API host port (default: automatic)")
	bindAll := flags.Bool("bind-all", false, "publish dashboard and API on 0.0.0.0 instead of localhost")
	noPull := flags.Bool("no-pull", false, "do not pull the image")
	noStart := flags.Bool("no-start", false, "do not start the container")
	rebuildOnStart := flags.Bool("rebuild-on-start", false, "regenerate docker-compose.yml on every start")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: hermes-manager install [OPTIONS] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	terminal.Info("Preparing Hermes instance at %s", rt.paths.Root)
	terminal.Info("Hermes state will live under data/ (HERMES_HOME). Project workspace/ is optional.")
	if *dashboardPort == 0 && *apiPort == 0 {
		terminal.Info("Host ports are automatic: free ports are chosen, skipping live listeners and sibling Hermes reservations.")
	}
	if *bindAll {
		terminal.Warn("Dashboard and API ports will listen on every host interface (0.0.0.0).")
	}
	start := !*noStart
	options := manager.InstallOptions{
		Name:          *name,
		Image:         *image,
		DashboardPort: *dashboardPort,
		APIPort:       *apiPort,
		BindAll:       *bindAll,
		Pull:          !*noPull,
		Start:         start,
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "rebuild-on-start" {
			options.HasRebuildComposeOnStart = true
			options.RebuildComposeOnStart = *rebuildOnStart
		}
	})
	result, err := rt.manager.Install(ctx, options)
	if err != nil {
		return err
	}
	switch {
	case result.Created && start:
		terminal.Success("Hermes installed and started")
	case result.Created:
		terminal.Success("Hermes installed (not started)")
		terminal.Info("Adjust docker-compose.yml or migrate data if needed, then run: hermes-manager start %q", rt.paths.Root)
	case start:
		terminal.Success("Hermes installation repaired and started")
	default:
		terminal.Success("Hermes installation repaired (not started)")
	}
	rt.printInstallSummary(result.Config)
	return nil
}

func (app *App) logsCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("logs", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	tail := flags.Int("tail", 100, "number of existing lines")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: hermes-manager logs [--tail N] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	return rt.manager.Logs(ctx, *tail)
}

func (app *App) shellCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	folderArgs := args
	commandArgs := []string(nil)
	for i, arg := range args {
		if arg == "--" {
			folderArgs = args[:i]
			commandArgs = args[i+1:]
			break
		}
	}
	root, err := oneFolder(folderArgs)
	if err != nil {
		return fmt.Errorf("usage: hermes-manager shell [FOLDER] [-- COMMAND...]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	return rt.manager.Shell(ctx, commandArgs...)
}

func (app *App) backupCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	label := flags.String("label", "manual", "short backup label")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: hermes-manager backup [--label LABEL] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	archive, err := rt.manager.Backup(ctx, *label)
	if err != nil {
		return err
	}
	terminal.Success("Backup created and verified: %s", archive)
	return nil
}

func (app *App) restoreCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: hermes-manager restore BACKUP.zip [FOLDER]")
	}
	root := "."
	if len(args) == 2 {
		root = args[1]
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	if err := terminal.RequirePhrase("Restore replaces Hermes configuration and user data. A safety backup is created first.", "RESTORE"); err != nil {
		return err
	}
	if err := rt.manager.Restore(ctx, args[0]); err != nil {
		return err
	}
	terminal.Success("Restore completed")
	return nil
}

func oneFolder(args []string) (string, error) {
	switch len(args) {
	case 0:
		return ".", nil
	case 1:
		return args[0], nil
	default:
		return "", fmt.Errorf("too many folder arguments")
	}
}

func (app *App) startCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	rebuild := flags.Bool("rebuild", false, "regenerate docker-compose.yml from the managed template before start")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: hermes-manager start [--rebuild] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	return rt.withSuccess("Hermes started", rt.manager.Start(ctx, manager.StartOptions{Rebuild: *rebuild}))
}

func (rt runtime) simpleCommand(ctx context.Context, commandName string) error {
	switch commandName {
	case "stop":
		return rt.withSuccess("Hermes stopped; persistent data was retained", rt.manager.Stop(ctx))
	case "restart":
		return rt.withSuccess("Hermes restarted", rt.manager.Restart(ctx))
	case "status":
		return rt.statusCommand(ctx)
	case "update":
		rt.ui.Info("Update recreates the container image only; host data/ is bind-mounted and kept.")
		confirmed, err := rt.ui.Confirm("Create a backup, pull the newest image, and update Hermes?", true)
		if err != nil {
			return err
		}
		if !confirmed {
			return fmt.Errorf("update cancelled")
		}
		return rt.withSuccess("Update completed and verified; host data was preserved", rt.manager.Update(ctx))
	case "rollback":
		if err := rt.ui.RequirePhrase("Rollback recreates Hermes with the previously recorded image.", "ROLLBACK"); err != nil {
			return err
		}
		return rt.withSuccess("Previous image restored", rt.manager.Rollback(ctx))
	default:
		return fmt.Errorf("unsupported command %q", commandName)
	}
}

func (rt runtime) statusCommand(ctx context.Context) error {
	status, err := rt.manager.Status(ctx)
	if err != nil {
		return err
	}
	rt.ui.Section("Hermes instance")
	rt.ui.KeyValue("Folder", status.Root)
	rt.ui.KeyValue("Name", status.Name)
	rt.ui.KeyValue("Image", status.Image)
	if status.Image != status.TrackedImage {
		rt.ui.KeyValue("Update channel", status.TrackedImage)
	}
	rt.ui.KeyValue("Dashboard", status.DashboardURL)
	rt.ui.KeyValue("Listening on", fmt.Sprintf("%s (dashboard %d, API %d)", status.BindAddress, status.DashboardPort, status.APIPort))
	if status.RebuildComposeOnStart {
		rt.ui.KeyValue("Compose on start", "rebuild full docker-compose.yml")
	} else {
		rt.ui.KeyValue("Compose on start", "preserve custom edits (use start --rebuild to force)")
	}
	rt.ui.KeyValue("API host port", status.APIPort)
	rt.ui.KeyValue("Hermes data", status.Data+"  (HERMES_HOME → /opt/data; critical, survives updates)")
	rt.ui.KeyValue("Agent workspace", rt.paths.HermesDataWorkspace()+"  (inside data/, created by Hermes)")
	rt.ui.KeyValue("Project workspace", status.Workspace+"  (optional host dir → /workspace)")
	rt.ui.KeyValue("Backups", status.Backups)
	rt.ui.Info("Updates recreate the container image only; host data/, workspace/, and backups/ are bind-mounted and kept.")
	if status.Version != "" {
		rt.ui.KeyValue("Version", status.Version)
	}
	if status.DashboardInfo != "" {
		if status.DashboardOK {
			rt.ui.KeyValue("Dashboard health", status.DashboardInfo)
		} else {
			rt.ui.Warn("Dashboard health: %s", status.DashboardInfo)
		}
	}
	fmt.Fprintln(rt.app.Out, "\n"+status.Containers)
	return nil
}

func (rt runtime) dashboardCommand() error {
	access, err := rt.manager.Dashboard()
	if err != nil {
		return err
	}
	rt.ui.Section("Dashboard access")
	rt.ui.KeyValue("URL", access.URL)
	rt.ui.KeyValue("Username", access.Username)
	rt.ui.KeyValue("Password", access.Password)
	rt.ui.KeyValue("Listening on", access.Listens)
	if strings.HasPrefix(access.Listens, "0.0.0.0:") {
		rt.ui.Warn("The dashboard is exposed on every host interface. Restrict access with a firewall, VPN, or authenticated reverse proxy.")
	} else {
		rt.ui.Warn("The dashboard is bound to localhost. Use an SSH tunnel for remote access.")
	}
	return nil
}

func (rt runtime) listBackups() error {
	files, err := rt.manager.ListBackups()
	if err != nil {
		return err
	}
	rt.ui.Section("Backups")
	if len(files) == 0 {
		rt.ui.Info("No backups found")
		return nil
	}
	for _, file := range files {
		fmt.Fprintln(rt.app.Out, "  "+file)
	}
	return nil
}

func (rt runtime) withSuccess(message string, err error) error {
	if err != nil {
		return err
	}
	rt.ui.Success("%s", message)
	return nil
}

func configDefaultImage() string {
	return "nousresearch/hermes-agent:latest"
}

func (app *App) printHelp() {
	fmt.Fprint(app.Out, `Hermes Manager — small, durable Docker lifecycle manager

Usage:
  hermes-manager [--yes] COMMAND [OPTIONS] [FOLDER]

Commands:
  install [options] [folder]     Create or repair an instance
  start [--rebuild] [folder]     Start the instance
  stop [folder]                  Stop it without deleting data
  restart [folder]               Restart the container
  status [folder]                Show ports, paths, image, and container state
  logs [--tail N] [folder]       Follow container logs
  shell [folder] [-- COMMAND...] Interactive shell inside the Hermes container
  dashboard [folder]             Show dashboard URL and login
  dashboard reset-password [folder]
                                 Rotate password and invalidate sessions
  backup [--label NAME] [folder] Create and verify a full Hermes backup
  backups [folder]               List backups
  backups prune [--keep N] [folder]
                                 Remove old automatic safety backups
  export-instance [--without-workspace] [folder]
                                 Export data, workspace, and recovery metadata
  restore BACKUP.zip [folder]    Restore after making a safety backup
  update [folder]                Backup, pull, recreate, and verify
  rollback [folder]              Recreate with the previous image
  doctor [folder]                Validate paths, mounts, Docker, and dashboard
  self-update [--check]          Update the Hermes Manager CLI from GitHub
  menu [folder]                  Open the interactive menu
  version                        Show build information

Install options:
  --name NAME                    Override the derived instance name
  --image IMAGE                  Override the official image
  --dashboard-port PORT          Host port; 0 auto-detects a free port
  --api-port PORT                Host port; 0 auto-detects a free port
  --bind-all                     Explicitly publish ports on 0.0.0.0
  --no-pull                      Generate without pulling the image
  --no-start                     Generate without starting the container
  --rebuild-on-start             Rebuild docker-compose.yml on every start

Port selection (new installs):
  Automatic mode avoids ports already listening and ports reserved by sibling
  Hermes Manager instances under the same parent folder. Interactive menu
  install offers detected free ports and lets you override them.

Start options:
  --rebuild                      Force-regenerate docker-compose.yml this start

Examples:
  hermes-manager install /srv/hermes/work
  hermes-manager install --no-start /srv/hermes/work
  hermes-manager install --rebuild-on-start /srv/hermes/work
  hermes-manager start --rebuild /srv/hermes/work
  hermes-manager install --dashboard-port 9120 /srv/hermes/second
  cd /srv/hermes/work && hermes-manager dashboard
  hermes-manager shell /srv/hermes/work
  hermes-manager shell /srv/hermes/work -- sh
  hermes-manager update /srv/hermes/work
  hermes-manager self-update

Hermes settings are managed in its web dashboard and persist in FOLDER/data.
`)
}
