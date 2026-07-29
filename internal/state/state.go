package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nicolaeser/HermesManager/internal/fsutil"
	"github.com/nicolaeser/HermesManager/internal/stack"
)

const SchemaVersion = 1

type State struct {
	SchemaVersion int       `json:"schema_version"`
	PreviousImage string    `json:"previous_image,omitempty"`
	LastBackup    string    `json:"last_backup,omitempty"`
	LastOperation string    `json:"last_operation,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Store struct {
	Paths stack.Paths
}

func (s Store) Load() (State, error) {
	content, err := os.ReadFile(s.Paths.State)
	if errors.Is(err, os.ErrNotExist) {
		return State{SchemaVersion: SchemaVersion}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read manager state: %w", err)
	}
	var value State
	if err := json.Unmarshal(content, &value); err != nil {
		return State{}, fmt.Errorf("parse manager state: %w", err)
	}
	if value.SchemaVersion != SchemaVersion {
		return State{}, fmt.Errorf("unsupported manager state schema %d", value.SchemaVersion)
	}
	return value, nil
}

func (s Store) Save(value State) error {
	value.SchemaVersion = SchemaVersion
	value.UpdatedAt = time.Now().UTC()
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manager state: %w", err)
	}
	content = append(content, '\n')
	return fsutil.AtomicWriteFile(s.Paths.State, content, 0o600)
}

func (s Store) Log(operation, detail string) error {
	if err := os.MkdirAll(s.Paths.Manager, 0o700); err != nil {
		return fmt.Errorf("create manager state directory: %w", err)
	}
	file, err := os.OpenFile(s.Paths.OperationsLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open operations log: %w", err)
	}
	defer file.Close()
	detail = strings.NewReplacer("\r", " ", "\n", " ").Replace(detail)
	_, err = fmt.Fprintf(file, "%s operation=%s %s\n", time.Now().UTC().Format(time.RFC3339), operation, detail)
	return err
}

type Lock struct {
	path     string
	released bool
}

func (s Store) Acquire(operation string) (*Lock, error) {
	if err := os.MkdirAll(s.Paths.Manager, 0o700); err != nil {
		return nil, fmt.Errorf("create manager directory: %w", err)
	}
	file, err := os.OpenFile(s.Paths.Lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			content, _ := os.ReadFile(s.Paths.Lock)
			return nil, fmt.Errorf("another operation holds %s (%s); use doctor --clear-stale-lock after verifying no manager process is active", s.Paths.Lock, strings.TrimSpace(string(content)))
		}
		return nil, fmt.Errorf("acquire operation lock: %w", err)
	}
	_, writeErr := fmt.Fprintf(file, "pid=%s operation=%s started=%s\n", strconv.Itoa(os.Getpid()), operation, time.Now().UTC().Format(time.RFC3339))
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(s.Paths.Lock)
		return nil, fmt.Errorf("write operation lock: %w", writeErr)
	}
	if closeErr != nil {
		_ = os.Remove(s.Paths.Lock)
		return nil, fmt.Errorf("close operation lock: %w", closeErr)
	}
	return &Lock{path: s.Paths.Lock}, nil
}

func (lock *Lock) Release() error {
	if lock == nil || lock.released {
		return nil
	}
	lock.released = true
	if err := os.Remove(lock.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("release operation lock: %w", err)
	}
	return nil
}

func (s Store) ClearLock() error {
	if err := os.Remove(s.Paths.Lock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear lock: %w", err)
	}
	return nil
}
