package discovery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FileWriter writes target groups to a file for Prometheus' file_sd_configs.
// It replaces the file atomically and only when the content changes, so
// Prometheus never reads a half-written file.
type FileWriter struct {
	Path string
	last []byte
}

// Write writes groups to the file if they differ from the last write.
func (f *FileWriter) Write(groups []TargetGroup) error {
	body, err := json.MarshalIndent(groups, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if f.last != nil && bytes.Equal(body, f.last) {
		return nil
	}
	dir := filepath.Dir(f.Path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(f.Path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("writing %s: %w", f.Path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", f.Path, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", f.Path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", f.Path, err)
	}
	if err := os.Rename(tmp.Name(), f.Path); err != nil {
		return fmt.Errorf("writing %s: %w", f.Path, err)
	}
	f.last = body
	return nil
}
