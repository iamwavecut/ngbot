package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/iamwavecut/ngbot/internal/db/sqlite"
	"github.com/iamwavecut/ngbot/internal/event"
	"github.com/iamwavecut/ngbot/internal/handlers"

	api "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/config"
	"github.com/iamwavecut/ngbot/internal/infra"
)

func main() {
	cfg := config.Get()
	log.SetFormatter(&config.NbFormatter{})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.Level(cfg.LogLevel))

	recoverable(func() {
		ctx, cancelFunc := context.WithCancel(context.Background())
		defer cancelFunc()
		defer event.RunWorker()()
		botAPI, err := api.NewBotAPI(cfg.TelegramAPIToken)
		if err != nil {
			log.WithError(err).Errorln("cant initialize bot api")
			time.Sleep(1 * time.Second)
			log.Fatalln("exiting")
		}
		if log.Level(cfg.LogLevel) == log.TraceLevel {
			botAPI.Debug = true
		}
		defer botAPI.StopReceivingUpdates()

		service := bot.NewService(botAPI, sqlite.NewSQLiteClient("bot.db"))

		bot.RegisterUpdateHandler("admin", handlers.NewAdmin(service))
		bot.RegisterUpdateHandler("gatekeeper", handlers.NewGatekeeper(service))
		bot.RegisterUpdateHandler("punto", handlers.NewPunto(service, infra.GetResourcesDir("punto")))

		updateConfig := api.NewUpdate(0)
		updateConfig.Timeout = 60
		updateProcessor := bot.NewUpdateProcessor(service)

		updateChan, errorChan := bot.GetUpdatesChans(botAPI, updateConfig)

		for {
			select {
			case err := <-errorChan:
				log.WithError(err).Fatalln("bot api get updates error")
			case update := <-updateChan:
				if err := updateProcessor.Process(&update); err != nil {
					log.WithError(err).Errorln("cant process update")
				}
			case <-ctx.Done():
				log.WithError(ctx.Err()).Errorln("no more updates")
				return
			}
		}
	})

	select {
	case <-infra.MonitorExecutable():
		log.Errorln("executable file was modified")
	}
	os.Exit(0)
}

func recoverable(f func()) {
	defer func() {
		if err := recover(); err != nil {
			log.Errorf(`panic with message: %s, %s\n`, err, identifyPanic())
			time.Sleep(5 * time.Second)
			go recoverable(f)
		}
	}()
	log.Debug("going recoverable")
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
