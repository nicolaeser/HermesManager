package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nicolaeser/HermesManager/internal/fsutil"
	"github.com/nicolaeser/HermesManager/internal/stack"
)

const (
	SchemaVersion      = 1
	DefaultImage       = "nousresearch/hermes-agent:latest"
	DefaultBindAddress = "127.0.0.1"
	PublicBindAddress  = "0.0.0.0"
)

type Config struct {
	SchemaVersion     int    `json:"schema_version"`
	Name              string `json:"name"`
	Image             string `json:"image"`
	PinnedImage       string `json:"pinned_image,omitempty"`
	BindAddress       string `json:"bind_address"`
	DashboardPort     int    `json:"dashboard_port"`
	APIPort           int    `json:"api_port"`
	DashboardUsername string `json:"dashboard_username"`
}

type Store struct {
	Paths stack.Paths
}

func New(root, name, image string, dashboardPort, apiPort int) Config {
	if strings.TrimSpace(name) == "" {
		name = InstanceName(root)
	}
	if strings.TrimSpace(image) == "" {
		image = DefaultImage
	}
	return Config{
		SchemaVersion:     SchemaVersion,
		Name:              name,
		Image:             image,
		BindAddress:       DefaultBindAddress,
		DashboardPort:     dashboardPort,
		APIPort:           apiPort,
		DashboardUsername: "admin",
	}
}

func (cfg Config) EffectiveImage() string {
	if cfg.PinnedImage != "" {
		return cfg.PinnedImage
	}
	return cfg.Image
}

func (s Store) Load() (Config, error) {
	content, err := os.ReadFile(s.Paths.Config)
	if err != nil {
		return Config{}, fmt.Errorf("read instance metadata: %w", err)
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", s.Paths.Config, err)
	}
	if cfg.BindAddress == "" {
		cfg.BindAddress = DefaultBindAddress
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate instance metadata: %w", err)
	}
	return cfg, nil
}

func (s Store) Save(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("refuse invalid instance metadata: %w", err)
	}
	content, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode instance metadata: %w", err)
	}
	content = append(content, '\n')
	if err := fsutil.AtomicWriteFile(s.Paths.Config, content, 0o600); err != nil {
		return fmt.Errorf("save instance metadata: %w", err)
	}
	return nil
}

func (s Store) Exists() bool {
	return fsutil.FileExists(s.Paths.Config)
}

var (
	namePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	imagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]*$`)
)

func (cfg Config) Validate() error {
	var problems []string
	if cfg.SchemaVersion != SchemaVersion {
		problems = append(problems, fmt.Sprintf("schema_version must be %d", SchemaVersion))
	}
	if !namePattern.MatchString(cfg.Name) {
		problems = append(problems, "name must contain only lowercase letters, numbers, and hyphens (maximum 63 characters)")
	}
	if !imagePattern.MatchString(cfg.Image) {
		problems = append(problems, "image contains unsupported characters")
	}
	if cfg.PinnedImage != "" && !imagePattern.MatchString(cfg.PinnedImage) {
		problems = append(problems, "pinned_image contains unsupported characters")
	}
	if cfg.BindAddress != DefaultBindAddress && cfg.BindAddress != PublicBindAddress {
		problems = append(problems, "bind_address must be 127.0.0.1 or 0.0.0.0")
	}
	if !validPort(cfg.DashboardPort) {
		problems = append(problems, "dashboard_port must be between 1 and 65535")
	}
	if !validPort(cfg.APIPort) {
		problems = append(problems, "api_port must be between 1 and 65535")
	}
	if cfg.DashboardPort == cfg.APIPort {
		problems = append(problems, "dashboard_port and api_port must differ")
	}
	if strings.TrimSpace(cfg.DashboardUsername) == "" ||
		strings.ContainsAny(cfg.DashboardUsername, "\r\n\x00=") {
		problems = append(problems, "dashboard_username must be a non-empty single-line value")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validPort(port int) bool {
	return port >= 1 && port <= 65535
}

func InstanceName(root string) string {
	absolute, err := filepath.Abs(root)
	if err != nil {
		absolute = filepath.Clean(root)
	}
	base := slug(filepath.Base(absolute))
	if base == "" {
		base = "instance"
	}
	sum := sha256.Sum256([]byte(filepath.Clean(absolute)))
	suffix := hex.EncodeToString(sum[:4])
	const prefix = "hermes-"
	maxBase := 63 - len(prefix) - 1 - len(suffix)
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
	}
	return prefix + base + "-" + suffix
}

func FolderHash(root string) uint16 {
	absolute, err := filepath.Abs(root)
	if err != nil {
		absolute = filepath.Clean(root)
	}
	sum := sha256.Sum256([]byte(filepath.Clean(absolute)))
	return uint16(sum[0])<<8 | uint16(sum[1])
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	lastDash := false
	for _, character := range value {
		valid := character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if valid {
			result.WriteRune(character)
			lastDash = false
		} else if !lastDash {
			result.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(result.String(), "-")
}
