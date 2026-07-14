package infra

import (
	"context"
	"fmt"
	"os"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	checkExecInterval = 5 * time.Second
)

func MonitorExecutable(ctx context.Context) (<-chan struct{}, error) {
	exeFilename, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	return monitorExecutable(ctx, exeFilename, checkExecInterval)
}

func monitorExecutable(ctx context.Context, exeFilename string, interval time.Duration) (<-chan struct{}, error) {
	stat, err := os.Stat(exeFilename)
	if err != nil {
		return nil, fmt.Errorf("stat executable %q: %w", exeFilename, err)
	}
	originalTime := stat.ModTime()
	ch := make(chan struct{}, 1)
	go func() {
		defer close(ch)
		log.Debug(exeFilename)

		ticker := time.NewTicker(interval)
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
	return ch, nil
}
