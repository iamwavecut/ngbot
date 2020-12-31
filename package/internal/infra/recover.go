package infra

import (
	"fmt"
	"runtime"
	"strings"

	log "github.com/sirupsen/logrus"
)

func GoRecoverable(maxPanics int, id string, f func()) {
	defer func() {
		if err := recover(); err != nil {
			log.Errorf(`Job "%s" panics with message: %s, %s\n`, id, err, identifyPanic())
			if maxPanics == 0 {
				log.Fatalf(`Panics limit exceeded for job "%s", exiting\n`, id)
			} else {
				if maxPanics > 0 {
					maxPanics--
					log.Debugf(`Recovering job "%s" with max panics left: %d\n`, id, maxPanics)
					go GoRecoverable(maxPanics, id, f)
				} else {
					log.Debugf(`Recovering job "%s"\n`, id)
					go GoRecoverable(maxPanics, id, f)
				}
			}
		}
	}()
	f()
}

func identifyPanic() string {
	var name, file string
	var line int
	var pc [16]uintptr

	n := runtime.Callers(3, pc[:])
	for _, pc := range pc[:n] {
		fn := runtime.FuncForPC(pc)
		if fn == nil {
			continue
		}
		file, line = fn.FileLine(pc)
		name = fn.Name()
		if !strings.HasPrefix(name, "runtime.") {
			break
		}
	}

	switch {
	case name != "":
		return fmt.Sprintf("%v:%v", name, line)
	case file != "":
		return fmt.Sprintf("%v:%v", file, line)
	}

	return fmt.Sprintf("pc:%x", pc)
}
