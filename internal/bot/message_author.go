package bot

import (
	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
)

func MessageAuthor(message *api.Message) (db.MessageAuthor, bool) {
	if message == nil {
		return db.MessageAuthor{}, false
	}
	var author db.MessageAuthor
	if message.SenderChat != nil {
		author = db.MessageAuthor{Kind: db.MessageAuthorSenderChat, ID: message.SenderChat.ID}
	} else if message.From != nil {
		author = db.MessageAuthor{Kind: db.MessageAuthorUser, ID: message.From.ID}
	}
	return author, author.Validate() == nil
}
