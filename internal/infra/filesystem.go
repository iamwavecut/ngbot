package infra

import (
	"os"
	"path/filepath"
	"strings"

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
	log.Println(workDir)
	return workDir
}

func GetResourcesPath(path ...string) string {
	return strings.Join(path, "/")
}
