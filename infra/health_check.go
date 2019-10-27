package infra

import (
	"os"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	checkExecInterval = 5 * time.Second
)

func checkExecLoop() {
	exeFilename, _ := os.Executable()
	log.Debug(exeFilename)
	stat, _ := os.Stat(exeFilename)
	exeModTime := stat.ModTime()
	for exeModTime.Equal(stat.ModTime()) {
		time.Sleep(checkExecInterval)
		stat, _ = os.Stat(exeFilename)
	}
	log.Fatalf("binary modified. old=%s new=%s", exeModTime, stat.ModTime())
}
