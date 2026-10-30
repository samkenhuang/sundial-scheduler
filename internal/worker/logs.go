package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// measureLogUsage sums regular files under the worker run-log directory.
// Central copies and spool files are not worker-local logs and are excluded.
func measureLogUsage(root string) (int64, error) {
	dir := filepath.Join(root, "runs")
	var total int64
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// cleanupLogs removes worker-local files older than before. It never follows
// symlinks and only touches root/runs, which is the Worker's own spool.
func cleanupLogs(root string, before time.Time) (deleted int64, bytes int64, err error) {
	dir := filepath.Join(root, "runs")
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(dir)+string(os.PathSeparator)) {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		if !info.ModTime().Before(before) {
			return nil
		}
		if removeErr := os.Remove(path); removeErr != nil {
			return fmt.Errorf("remove %s: %w", path, removeErr)
		}
		deleted++
		bytes += info.Size()
		return nil
	})
	if err != nil {
		return deleted, bytes, err
	}
	return deleted, bytes, nil
}
