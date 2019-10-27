package infra

import (
	"os"
	"path/filepath"

	"github.com/mitchellh/go-homedir"
	log "github.com/sirupsen/logrus"
)

func GetWorkDir() string {
	workDir, err := homedir.Expand(filepath.Join("~", ".plotva"))
	if err != nil {
		log.Fatalln(err)
	}
	if err = os.MkdirAll(workDir, os.ModePerm); err != nil {
		log.Fatalln(err)
	}

	return workDir
}
