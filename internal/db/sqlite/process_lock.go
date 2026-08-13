package sqlite

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type ProcessLock struct {
	file *os.File
}

func AcquireProcessLock(dataDir string) (*ProcessLock, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create process lock directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(dataDir, "ngbot.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open process lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("ngbot database is in use")
		}
		return nil, fmt.Errorf("acquire process lock: %w", err)
	}
	return &ProcessLock{file: file}, nil
}

func (l *ProcessLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	return errors.Join(unlockErr, closeErr)
}
