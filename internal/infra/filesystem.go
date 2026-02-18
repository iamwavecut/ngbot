package infra

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mitchellh/go-homedir"
)

func EnsureDir(path string) (string, error) {
	workDir, err := homedir.Expand(path)
	if err != nil {
		return "", fmt.Errorf("expand path %q: %w", path, err)
	}
	if err := os.MkdirAll(workDir, os.ModePerm); err != nil {
		return "", fmt.Errorf("create dir %q: %w", workDir, err)
	}
	absPath, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve abs path %q: %w", workDir, err)
	}
	return absPath, nil
}

func GetResourcesPath(path ...string) string {
	return strings.Join(path, "/")
}
