package migrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/timoxue/loom-cli/internal/engine"
)

// writeSkill atomically writes a v1 skill to disk. Uses tempfile +
// rename to avoid leaving a half-written `.loom.json` if the process
// is killed mid-write.
func writeSkill(targetPath string, skill *engine.LoomSkill) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create parent: %w", err)
	}

	raw, err := jsonMarshalIndent(skill)
	if err != nil {
		return err
	}

	tempFile, err := os.CreateTemp(filepath.Dir(targetPath), ".loom-migrate-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tempPath := tempFile.Name()
	success := false
	defer func() {
		if success {
			return
		}
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
	}()

	if _, err := tempFile.Write(raw); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tempFile.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tempPath, targetPath); err != nil {
		// On Windows, rename fails if the target exists; try remove then rename.
		if removeErr := os.Remove(targetPath); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("remove existing target before rename: %w", removeErr)
		}
		if err := os.Rename(tempPath, targetPath); err != nil {
			return fmt.Errorf("rename into place: %w", err)
		}
	}
	success = true
	return nil
}

func jsonMarshalIndent(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	raw = append(raw, '\n')
	return raw, nil
}
