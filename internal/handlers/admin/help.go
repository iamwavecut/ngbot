package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	log "github.com/sirupsen/logrus"

	"github.com/iamwavecut/ngbot/internal/bot"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

func (a *Admin) handleHelpCommand(ctx context.Context, msg *api.Message, chat *api.Chat, lang string) error {
	if msg == nil || chat == nil {
		return nil
	}
	if chat.Type == panelChatTypePrivate {
		return a.sendPrivateHelp(ctx, msg.Chat.ID, lang)
	}
	return a.sendGroupHelpBridge(ctx, msg, lang)
}

func (a *Admin) sendPrivateHelp(ctx context.Context, chatID int64, lang string) error {
	help := api.NewMessage(chatID, a.renderHelpMarkdown(lang))
	help.ParseMode = api.ModeMarkdownV2
	help.DisableNotification = true
	help.LinkPreviewOptions.IsDisabled = true
	_, err := bot.Send(ctx, a.bot, help)
	return err
}

func (a *Admin) sendGroupHelpBridge(ctx context.Context, msg *api.Message, lang string) error {
	text := markdownV2Italic(i18n.Get("Help is available in private chat. Use the button below to open it.", lang))
	reply := api.NewMessage(msg.Chat.ID, text)
	reply.ParseMode = api.ModeMarkdownV2
	reply.DisableNotification = true
	reply.LinkPreviewOptions.IsDisabled = true
	reply.MessageThreadID = msg.MessageThreadID
	reply.ReplyParameters.MessageID = msg.MessageID
	reply.ReplyParameters.ChatID = msg.Chat.ID
	reply.ReplyParameters.AllowSendingWithoutReply = true

	link := fmt.Sprintf("https://t.me/%s?start=help", a.botUsername())
	keyboard := api.NewInlineKeyboardMarkup(
		api.NewInlineKeyboardRow(
			api.NewInlineKeyboardButtonURL(i18n.Get("Open help in private chat", lang), link),
		),
	)
	reply.ReplyMarkup = &keyboard

	sent, err := bot.Send(ctx, a.bot, reply)
	if err != nil {
		return err
	}

	delay := a.temporaryTTL()
	a.deleteMessageAfter(msg.Chat.ID, sent.MessageID, delay)
	a.deleteMessageAfter(msg.Chat.ID, msg.MessageID, delay)

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (a *Admin) renderHelpMarkdown(lang string) string {
	botMention := "@" + a.botUsername()
	var builder strings.Builder

	builder.WriteString(markdownV2Bold(i18n.Get("Help", lang)))
	builder.WriteString("\n")
	builder.WriteString(markdownV2Italic(i18n.Get("This is the /help command output. You can open it in private chat at any time.", lang)))
	builder.WriteString("\n\n")

	writeHelpSection(&builder, i18n.Get("Gatekeeper Settings", lang))
	writeHelpBullet(&builder, i18n.Get("Checks join requests and new members with CAPTCHA or greeting when enabled.", lang))
	writeHelpBullet(&builder, i18n.Get("Checks known spammers with LoLs bot, CAS, Combot, and the local banlist.", lang))
	builder.WriteString("\n")

	writeHelpSection(&builder, i18n.Get("New-user message probation", lang)+" / "+i18n.Get("Reaction Profile Check", lang))
	writeHelpBullet(&builder, i18n.Get("Runs new-user message probation and reaction profile checks when they are enabled.", lang))
	builder.WriteString("\n")

	writeHelpSection(&builder, i18n.Get("Community Voting", lang))
	writeHelpBulletMarkdown(&builder, fmt.Sprintf(
		markdownV2Text(i18n.Get("Reply with %s or mention %s in a reply to report spam.", lang)),
		markdownV2Code("/voteban"),
		markdownV2Code(botMention),
	))
	writeHelpBullet(&builder, i18n.Get("The bot re-checks the reported message. Confirmed spam is removed and the user is banned; uncertain cases go to community voting.", lang))
	writeHelpBullet(&builder, i18n.Get("Voting uses the chat limits from settings, and the suspected user cannot vote on their own case.", lang))
	builder.WriteString("\n")

	writeHelpSection(&builder, i18n.Get("Settings", lang))
	writeHelpBulletMarkdown(&builder, fmt.Sprintf(
		markdownV2Text(i18n.Get("Group admins run %s in a group to open the private admin panel.", lang)),
		markdownV2Code("/settings"),
	))
	writeHelpBullet(&builder, i18n.Get("The panel configures language, gatekeeper, CAPTCHA and greeting, LLM checks, voting limits, spam examples, and not-spammer overrides.", lang))
	builder.WriteString("\n")

	writeHelpSection(&builder, i18n.Get("Commands", lang))
	writeHelpBulletMarkdown(&builder, fmt.Sprintf(
		markdownV2Text(i18n.Get("%s opens this help, %s reports spam by reply, %s reports by mention, %s opens settings for group admins, and %s changes language.", lang)),
		markdownV2Code("/help"),
		markdownV2Code("/voteban"),
		markdownV2Code(botMention),
		markdownV2Code("/settings"),
		markdownV2Code("/lang <code>"),
	))

	return builder.String()
}

func writeHelpSection(builder *strings.Builder, title string) {
	builder.WriteString(markdownV2Bold(title))
	builder.WriteString("\n")
}

func writeHelpBullet(builder *strings.Builder, text string) {
	writeHelpBulletMarkdown(builder, markdownV2Text(text))
}

func writeHelpBulletMarkdown(builder *strings.Builder, text string) {
	builder.WriteString("\\- ")
	builder.WriteString(text)
	builder.WriteString("\n")
}

func markdownV2Text(text string) string {
	return api.EscapeText(api.ModeMarkdownV2, text)
}

func markdownV2Bold(text string) string {
	return "*" + markdownV2Text(text) + "*"
}

func markdownV2Italic(text string) string {
	return "_" + markdownV2Text(text) + "_"
}

func markdownV2Code(text string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "`", "\\`")
	return "`" + replacer.Replace(text) + "`"
}

func (a *Admin) botUsername() string {
	b := a.bot
	if b == nil || b.Self.UserName == "" {
		return "bot"
	}
	return b.Self.UserName
}

func (a *Admin) temporaryTTL() time.Duration {
	if a.temporaryMessageTTL > 0 {
		return a.temporaryMessageTTL
	}
	return time.Minute
}

func (a *Admin) deleteMessageAfter(chatID int64, messageID int, delay time.Duration) {
	if messageID == 0 {
		return
	}
	a.scheduleAfter(delay, func(runCtx context.Context) {
		if err := bot.DeleteChatMessage(runCtx, a.bot, chatID, messageID); err != nil {
			log.WithField("error", err.Error()).WithField("chat_id", chatID).WithField("message_id", messageID).Error("failed to delete scheduled admin message")
		}
	})
}

func (a *Admin) scheduleAfter(delay time.Duration, task func(context.Context)) {
	runCtx := a.runtimeContext()
	a.wg.Go(func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()

		select {
		case <-runCtx.Done():
			return
		case <-timer.C:
			task(runCtx)
		}
	})
}

func (a *Admin) runtimeContext() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.runCtx != nil {
		return a.runCtx
	}
	return context.Background()
}
