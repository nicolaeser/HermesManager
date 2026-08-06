package compose

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/nicolaeser/HermesManager/internal/config"
	"github.com/nicolaeser/HermesManager/internal/fsutil"
	"github.com/nicolaeser/HermesManager/internal/secrets"
	"github.com/nicolaeser/HermesManager/internal/stack"
)

type Generator struct {
	Paths stack.Paths
}

func (generator Generator) Prepare(cfg config.Config, secretValues secrets.Values, force bool) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	for _, key := range []string{
		secrets.DashboardUsername,
		secrets.DashboardPassword,
		secrets.DashboardSecret,
	} {
		if secretValues[key] == "" {
			return fmt.Errorf("required dashboard credential %s is missing", key)
		}
	}

	for _, directory := range []struct {
		path string
		mode os.FileMode
	}{
		{generator.Paths.Manager, 0o700},
		{generator.Paths.Data, 0o700},
		{generator.Paths.Workspace, 0o755},
		{generator.Paths.Backups, 0o700},
	} {
		if err := ensureDirectory(directory.path, directory.mode); err != nil {
			return err
		}
	}

	desired := Render(cfg)
	existingPath := ""
	if fsutil.FileExists(generator.Paths.Compose) {
		existingPath = generator.Paths.Compose
	} else if fsutil.FileExists(generator.Paths.LegacyCompose()) {
		existingPath = generator.Paths.LegacyCompose()
	}

	if existingPath == "" {
		if err := fsutil.AtomicWriteFile(generator.Paths.Compose, desired, 0o600); err != nil {
			return fmt.Errorf("write Compose file: %w", err)
		}
		return nil
	}

	existing, err := os.ReadFile(existingPath)
	if err != nil {
		return fmt.Errorf("read Compose file: %w", err)
	}

	var content []byte
	switch {
	case force:
		content = desired
	case bytes.Equal(existing, desired):
		content = existing
	default:
		content = SyncManaged(existing, cfg)
	}

	if existingPath != generator.Paths.Compose || !bytes.Equal(existing, content) {
		if err := fsutil.AtomicWriteFile(generator.Paths.Compose, content, 0o600); err != nil {
			return fmt.Errorf("write Compose file: %w", err)
		}
	}
	if existingPath == generator.Paths.LegacyCompose() || fsutil.FileExists(generator.Paths.LegacyCompose()) {
		_ = os.Remove(generator.Paths.LegacyCompose())
	}
	return nil
}

func Render(cfg config.Config) []byte {
	var output strings.Builder
	output.WriteString("services:\n")
	output.WriteString("  hermes:\n")
	fmt.Fprintf(&output, "    image: %s\n", yamlQuote(cfg.EffectiveImage()))
	fmt.Fprintf(&output, "    container_name: %s\n", yamlQuote(cfg.Name))
	output.WriteString("    restart: unless-stopped\n")
	output.WriteString("    command: [\"gateway\", \"run\"]\n")
	output.WriteString("    working_dir: /workspace\n")
	output.WriteString("    stop_grace_period: 1m\n")
	output.WriteString("    env_file:\n")
	output.WriteString("      - ./.manager/secrets.env\n")
	output.WriteString("    environment:\n")
	fmt.Fprintf(&output, "      HERMES_UID: %s\n", yamlQuote(strconv.Itoa(os.Getuid())))
	fmt.Fprintf(&output, "      HERMES_GID: %s\n", yamlQuote(strconv.Itoa(os.Getgid())))
	output.WriteString("      HERMES_HOME: \"/opt/data\"\n")
	output.WriteString("      HERMES_WRITE_SAFE_ROOT: \"/opt/data\"\n")
	output.WriteString("      HERMES_DASHBOARD: \"1\"\n")
	output.WriteString("      HERMES_DASHBOARD_HOST: \"0.0.0.0\"\n")
	output.WriteString("      HERMES_DASHBOARD_PORT: \"9119\"\n")
	output.WriteString("      HERMES_GATEWAY_BOOTSTRAP_STATE: \"running\"\n")
	output.WriteString("    ports:\n")
	fmt.Fprintf(&output, "      - %s\n", yamlQuote(fmt.Sprintf("%s:%d:9119", cfg.BindAddress, cfg.DashboardPort)))
	fmt.Fprintf(&output, "      - %s\n", yamlQuote(fmt.Sprintf("%s:%d:8642", cfg.BindAddress, cfg.APIPort)))
	output.WriteString("    volumes:\n")
	output.WriteString("      - ./data:/opt/data\n")
	output.WriteString("      - ./workspace:/workspace\n")
	output.WriteString("      - ./backups:/backups\n")
	output.WriteString("    healthcheck:\n")
	output.WriteString("      test: [\"CMD\", \"python\", \"-c\", \"import json,urllib.request; d=json.load(urllib.request.urlopen('http://127.0.0.1:9119/api/health',timeout=3)); assert d.get('ok') and d.get('auth_required')\"]\n")
	output.WriteString("      interval: 30s\n")
	output.WriteString("      timeout: 5s\n")
	output.WriteString("      retries: 3\n")
	output.WriteString("      start_period: 45s\n")
	output.WriteString("    logging:\n")
	output.WriteString("      driver: json-file\n")
	output.WriteString("      options:\n")
	output.WriteString("        max-size: \"10m\"\n")
	output.WriteString("        max-file: \"3\"\n")
	return []byte(output.String())
}

func SyncManaged(existing []byte, cfg config.Config) []byte {
	text := string(existing)
	endsWithNL := strings.HasSuffix(text, "\n")
	if endsWithNL {
		text = strings.TrimSuffix(text, "\n")
	}
	lines := strings.Split(text, "\n")
	uid := yamlQuote(strconv.Itoa(os.Getuid()))
	gid := yamlQuote(strconv.Itoa(os.Getgid()))
	image := yamlQuote(cfg.EffectiveImage())
	name := yamlQuote(cfg.Name)
	dashboardPort := yamlQuote(fmt.Sprintf("%s:%d:9119", cfg.BindAddress, cfg.DashboardPort))
	apiPort := yamlQuote(fmt.Sprintf("%s:%d:8642", cfg.BindAddress, cfg.APIPort))

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		switch {
		case strings.HasPrefix(trimmed, "image:"):
			lines[i] = indent + "image: " + image
		case strings.HasPrefix(trimmed, "container_name:"):
			lines[i] = indent + "container_name: " + name
		case strings.HasPrefix(trimmed, "HERMES_UID:"):
			lines[i] = indent + "HERMES_UID: " + uid
		case strings.HasPrefix(trimmed, "HERMES_GID:"):
			lines[i] = indent + "HERMES_GID: " + gid
		case isPublishedPort(trimmed, 9119):
			lines[i] = indent + "- " + dashboardPort
		case isPublishedPort(trimmed, 8642):
			lines[i] = indent + "- " + apiPort
		}
	}

	out := strings.Join(lines, "\n")
	if endsWithNL {
		out += "\n"
	}
	return []byte(out)
}

func isPublishedPort(trimmed string, containerPort int) bool {
	if !strings.HasPrefix(trimmed, "-") {
		return false
	}
	value := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
	value = strings.Trim(value, `"'`)
	if strings.Contains(value, "://") {
		return false
	}
	suffix := fmt.Sprintf(":%d", containerPort)
	if !strings.HasSuffix(value, suffix) {
		return false
	}
	return strings.Count(value, ":") >= 1
}

func ensureDirectory(path string, mode os.FileMode) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s exists but is not a directory", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("set permissions on %s: %w", path, err)
	}
	return nil
}

func yamlQuote(value string) string {
	return strconv.Quote(value)
}
