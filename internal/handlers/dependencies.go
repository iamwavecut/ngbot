package handlers

import (
	"github.com/iamwavecut/ngbot/internal/bot"
	adminhandler "github.com/iamwavecut/ngbot/internal/handlers/admin"
	chathandler "github.com/iamwavecut/ngbot/internal/handlers/chat"
)

// Ensure all handlers implement the Handler interface
var _ bot.Handler = (*adminhandler.Admin)(nil)
var _ bot.Handler = (*chathandler.Gatekeeper)(nil)
var _ bot.Handler = (*chathandler.Reactor)(nil)
