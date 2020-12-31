package infra

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mitchellh/go-homedir"
	"github.com/otiai10/copy"
	log "github.com/sirupsen/logrus"
)

var resourcesDir string
var configFilepath string

func GetHomeDir(path ...string) string {
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

func InitConfig(file string) {
	if file == "" {
		file = "./config.yml"
	}
	absFile, err := filepath.Abs(file)
	if err != nil {
		log.WithError(err).Warn("no config path")
	}
	configDir := filepath.Dir(absFile)
	distLog := log.WithField("context", "init_config")

	empty, err := IsEmptyDir(configDir)
	if err != nil {
		distLog.WithError(err).Fatalln("check")
	}
	if empty {
		if err = copy.Copy("dist/etc", configDir, copy.Options{
			Skip: func(src string) (bool, error) {
				return strings.HasSuffix(src, "."), nil
			},
		}); err != nil {
			distLog.WithError(err).Fatalln("copy")
		}
		distLog.Debug("success")
	}

	configFilepath = file
}

func InitResourcesPath(path string) {
	var base []string
	if path != "" {
		base = append(base, path)
	} else {
		if wd, err := os.Getwd(); err == nil {
			base = append(base, wd, "resources")
			baseStr := filepath.Join(base...)
			distLog := log.WithField("context", "init_resources")

			empty, err := IsEmptyDir(baseStr)
			if err != nil {
				distLog.WithError(err).Fatalln("check")
			}
			if empty {
				if err = copy.Copy("dist/resources", baseStr, copy.Options{
					Skip: func(src string) (bool, error) {
						return strings.HasSuffix(src, "."), nil
					},
				}); err != nil {
					distLog.WithError(err).Fatalln("copy")
				}
				distLog.Debug("success")
			}
		}
	}

	resourcesDir = filepath.Join(base...)
}

func GetConfig() string {
	return configFilepath
}

func GetResourcesDir(path ...string) string {
	base := append([]string{resourcesDir}, path...)
	return filepath.Join(base...)
}

func IsEmptyDir(name string) (bool, error) {
	f, err := os.Open(name)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	_, err = f.Readdirnames(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err
}
