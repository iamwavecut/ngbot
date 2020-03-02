package infra

import (
	"os"
	"path/filepath"

	"github.com/mitchellh/go-homedir"
	log "github.com/sirupsen/logrus"
)

func GetWorkDir(path ...string) string {
	parts := []string{
		"~",
		".ngbot",
	}
	parts = append(parts, path...)
	workDir, err := homedir.Expand(filepath.Join(parts...))
	if err != nil {
		log.Fatalln(err)
	}
	if err = os.MkdirAll(workDir, os.ModePerm); err != nil {
		log.Fatalln(err)
	}
	return workDir
}

func GetResourcesDir(path ...string) string {
	var parts []string
	if envPath := os.Getenv("NGBOT_RESOURCES_PATH"); envPath != "" {
		parts = append(parts, envPath)
	} else {
		if wd, err := os.Getwd(); err == nil {
			parts = append(parts, wd)
		}
	}
	parts = append(parts, path...)
	return filepath.Join(parts...)
}
