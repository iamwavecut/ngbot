package infra

import (
	"context"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	checkExecInterval = 5 * time.Second
)

func MonitorExecutable(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		defer close(ch)

		exeFilename, err := os.Executable()
		if err != nil {
			log.WithError(err).Warn("cant resolve executable path for monitor")
			return
		}
		log.Debug(exeFilename)
		stat, err := os.Stat(exeFilename)
		if err != nil {
			log.WithError(err).Warn("cant stat executable for monitor")
			return
		}
		originalTime := stat.ModTime()

		ticker := time.NewTicker(checkExecInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stat, err := os.Stat(exeFilename)
				if err != nil {
					log.WithError(err).Warn("cant stat executable for monitor tick")
					continue
				}
				if !originalTime.Equal(stat.ModTime()) {
					ch <- struct{}{}
					return
				}
			}
		}
	}()
	return ch
}
