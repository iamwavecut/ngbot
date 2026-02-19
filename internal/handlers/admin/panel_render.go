package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	api "github.com/OvyFlash/telegram-bot-api"
	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

const (
	panelGreetingPlaceholderUser           = "{user}"
	panelGreetingPlaceholderChatTitle      = "{chat_title}"
	panelGreetingPlaceholderChatLinkTitled = "{chat_link_titled}"
	panelGreetingPlaceholderTimeout        = "{timeout}"
)

func (a *Admin) renderHome(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	title := i18n.Get("Settings", lang)
	chatTitle := state.ChatTitle
	if chatTitle == "" {
		chatTitle = i18n.Get("Unknown chat", lang)
	}
	body := fmt.Sprintf("%s\n\n%s: %s\n%s: %d",
		title,
		i18n.Get("Chat", lang),
		chatTitle,
		i18n.Get("Chat ID", lang),
		state.ChatID,
	)

	languageLabelText := fmt.Sprintf("%s: %s", i18n.Get("Language", lang), languageLabel(state.Language))
	languageBtn, err := a.commandButton(ctx, session.ID, languageLabelText, panelCommand{Action: panelActionOpenLanguage})
	if err != nil {
		return "", nil, err
	}

	gatekeeperLabel := fmt.Sprintf("%s %s", statusEmoji(state.Features.GatekeeperEffective), i18n.Get("Gatekeeper", lang))
	gatekeeperBtn, err := a.commandButton(ctx, session.ID, gatekeeperLabel, panelCommand{Action: panelActionOpenGatekeeper})
	if err != nil {
		return "", nil, err
	}

	llmLabel := fmt.Sprintf("%s %s", statusEmoji(state.Features.LLMFirstMessageEnabled), i18n.Get("LLM First Message", lang))
	llmBtn, err := a.commandButton(ctx, session.ID, llmLabel, panelCommand{Action: panelActionOpenLLM})
	if err != nil {
		return "", nil, err
	}

	votingLabel := fmt.Sprintf("%s %s", statusEmoji(state.Features.CommunityVotingEnabled), i18n.Get("Community Voting", lang))
	votingBtn, err := a.commandButton(ctx, session.ID, votingLabel, panelCommand{Action: panelActionOpenVoting})
	if err != nil {
		return "", nil, err
	}

	closeBtn, err := a.commandButton(ctx, session.ID, "❌", panelCommand{Action: panelActionClose})
	if err != nil {
		return "", nil, err
	}

	keyboard := api.NewInlineKeyboardMarkup(
		api.NewInlineKeyboardRow(languageBtn),
		api.NewInlineKeyboardRow(gatekeeperBtn),
		api.NewInlineKeyboardRow(llmBtn),
		api.NewInlineKeyboardRow(votingBtn),
		api.NewInlineKeyboardRow(closeBtn),
	)

	return body, &keyboard, nil
}

func (a *Admin) renderLanguageList(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	allLanguages := i18n.GetLanguagesList()
	totalPages := pageCount(len(allLanguages), panelLanguagePageSize)
	state.LanguagePage = clampPage(state.LanguagePage, totalPages)

	start := state.LanguagePage * panelLanguagePageSize
	end := minInt(start+panelLanguagePageSize, len(allLanguages))
	pageLangs := allLanguages
	if start < end {
		pageLangs = allLanguages[start:end]
	} else {
		pageLangs = []string{}
	}

	builder := strings.Builder{}
	builder.WriteString(i18n.Get("Language", lang))
	builder.WriteString("\n")
	builder.WriteString(fmt.Sprintf(i18n.Get("Page %d/%d", lang), state.LanguagePage+1, totalPages))
	builder.WriteString(" • ")
	builder.WriteString(fmt.Sprintf(i18n.Get("Total languages: %d", lang), len(allLanguages)))
	builder.WriteString("\n\n")
	if len(pageLangs) == 0 {
		builder.WriteString(i18n.Get("No languages available", lang))
	} else {
		for i, code := range pageLangs {
			name := i18n.GetLanguageName(code)
			label := fmt.Sprintf("%d. %s (%s)", start+i+1, name, code)
			if code == state.Language {
				label = "✅ " + label
			}
			builder.WriteString(label)
			builder.WriteString("\n")
		}
	}

	var buttons []api.InlineKeyboardButton
	for _, code := range pageLangs {
		name := i18n.GetLanguageName(code)
		label := fmt.Sprintf("%s (%s)", name, code)
		if code == state.Language {
			label = "✅ " + label
		}
		btn, err := a.commandButton(ctx, session.ID, label, panelCommand{Action: panelActionSelectLanguage, Language: code})
		if err != nil {
			return "", nil, err
		}
		buttons = append(buttons, btn)
	}

	rows := chunkButtons(buttons, 2)
	navRow, err := a.navRow(ctx, session.ID, panelActionLanguagePagePrev, panelActionLanguagePageNext, state.LanguagePage > 0, state.LanguagePage < totalPages-1)
	if err != nil {
		return "", nil, err
	}
	rows = append(rows, navRow)

	keyboard := api.NewInlineKeyboardMarkup(rows...)
	return builder.String(), &keyboard, nil
}

func (a *Admin) renderGatekeeper(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	text := fmt.Sprintf(
		"%s\n\n%s %s",
		i18n.Get("Gatekeeper Settings", lang),
		statusEmoji(state.Features.GatekeeperEnabled),
		i18n.Get("Master Switch", lang),
	)

	masterBtn, err := a.commandButton(ctx, session.ID, fmt.Sprintf("%s %s", statusEmoji(state.Features.GatekeeperEnabled), i18n.Get("Master Switch", lang)), panelCommand{Action: panelActionGatekeeperToggleMaster})
	if err != nil {
		return "", nil, err
	}
	captchaBtn, err := a.commandButton(ctx, session.ID, i18n.Get("CAPTCHA Settings", lang), panelCommand{Action: panelActionOpenGatekeeperCaptcha})
	if err != nil {
		return "", nil, err
	}
	greetingBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Greeting Settings", lang), panelCommand{Action: panelActionOpenGatekeeperGreeting})
	if err != nil {
		return "", nil, err
	}
	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}

	keyboard := api.NewInlineKeyboardMarkup(
		api.NewInlineKeyboardRow(masterBtn),
		api.NewInlineKeyboardRow(captchaBtn),
		api.NewInlineKeyboardRow(greetingBtn),
		api.NewInlineKeyboardRow(backBtn),
	)
	return text, &keyboard, nil
}

func (a *Admin) renderGatekeeperCaptcha(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	text := fmt.Sprintf(
		"%s\n\n%s %s\n%s: %d\n%s: %s\n%s: %s",
		i18n.Get("CAPTCHA Settings", lang),
		statusEmoji(state.Features.GatekeeperCaptchaEnabled),
		i18n.Get("CAPTCHA", lang),
		i18n.Get("Captcha options", lang),
		state.GatekeeperCaptchaOptionsCount,
		i18n.Get("Challenge timeout", lang),
		panelDurationLabel(time.Duration(state.ChallengeTimeout)),
		i18n.Get("Reject timeout", lang),
		panelDurationLabel(time.Duration(state.RejectTimeout)),
	)

	toggleBtn, err := a.commandButton(ctx, session.ID, fmt.Sprintf("%s %s", statusEmoji(state.Features.GatekeeperCaptchaEnabled), i18n.Get("CAPTCHA", lang)), panelCommand{Action: panelActionGatekeeperToggleCaptcha})
	if err != nil {
		return "", nil, err
	}
	optionsBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Captcha options", lang), panelCommand{Action: panelActionOpenGatekeeperCaptchaOptions})
	if err != nil {
		return "", nil, err
	}
	challengeBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Challenge timeout", lang), panelCommand{Action: panelActionOpenGatekeeperChallengeTimeout})
	if err != nil {
		return "", nil, err
	}
	rejectBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Reject timeout", lang), panelCommand{Action: panelActionOpenGatekeeperRejectTimeout})
	if err != nil {
		return "", nil, err
	}
	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}

	keyboard := api.NewInlineKeyboardMarkup(
		api.NewInlineKeyboardRow(toggleBtn),
		api.NewInlineKeyboardRow(optionsBtn),
		api.NewInlineKeyboardRow(challengeBtn),
		api.NewInlineKeyboardRow(rejectBtn),
		api.NewInlineKeyboardRow(backBtn),
	)
	return text, &keyboard, nil
}

func (a *Admin) renderGatekeeperCaptchaOptions(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	text := fmt.Sprintf("%s\n\n%s: %d", i18n.Get("Captcha options", lang), i18n.Get("Selected", lang), state.GatekeeperCaptchaOptionsCount)

	buttons := make([]api.InlineKeyboardButton, 0, len(panelGatekeeperCaptchaOptions))
	for _, option := range panelGatekeeperCaptchaOptions {
		label := fmt.Sprintf("%d", option)
		if option == state.GatekeeperCaptchaOptionsCount {
			label = "✅ " + label
		}
		btn, err := a.commandButton(ctx, session.ID, label, panelCommand{Action: panelActionGatekeeperSetCaptchaSize, Value: strconv.Itoa(option)})
		if err != nil {
			return "", nil, err
		}
		buttons = append(buttons, btn)
	}
	rows := chunkButtons(buttons, 3)
	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}
	rows = append(rows, api.NewInlineKeyboardRow(backBtn))
	keyboard := api.NewInlineKeyboardMarkup(rows...)
	return text, &keyboard, nil
}

func (a *Admin) renderGatekeeperChallengeTimeout(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	text := fmt.Sprintf("%s\n\n%s: %s", i18n.Get("Challenge timeout", lang), i18n.Get("Selected", lang), panelDurationLabel(time.Duration(state.ChallengeTimeout)))

	buttons := make([]api.InlineKeyboardButton, 0, len(panelChallengeTimeoutOptions))
	for _, option := range panelChallengeTimeoutOptions {
		label := panelDurationLabel(option)
		if time.Duration(state.ChallengeTimeout) == option {
			label = "✅ " + label
		}
		btn, err := a.commandButton(ctx, session.ID, label, panelCommand{Action: panelActionGatekeeperSetChallengeTTL, Value: option.String()})
		if err != nil {
			return "", nil, err
		}
		buttons = append(buttons, btn)
	}
	rows := chunkButtons(buttons, 3)
	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}
	rows = append(rows, api.NewInlineKeyboardRow(backBtn))
	keyboard := api.NewInlineKeyboardMarkup(rows...)
	return text, &keyboard, nil
}

func (a *Admin) renderGatekeeperRejectTimeout(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	text := fmt.Sprintf("%s\n\n%s: %s", i18n.Get("Reject timeout", lang), i18n.Get("Selected", lang), panelDurationLabel(time.Duration(state.RejectTimeout)))

	buttons := make([]api.InlineKeyboardButton, 0, len(panelRejectTimeoutOptions))
	for _, option := range panelRejectTimeoutOptions {
		label := panelDurationLabel(option)
		if time.Duration(state.RejectTimeout) == option {
			label = "✅ " + label
		}
		btn, err := a.commandButton(ctx, session.ID, label, panelCommand{Action: panelActionGatekeeperSetRejectTTL, Value: option.String()})
		if err != nil {
			return "", nil, err
		}
		buttons = append(buttons, btn)
	}
	rows := chunkButtons(buttons, 3)
	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}
	rows = append(rows, api.NewInlineKeyboardRow(backBtn))
	keyboard := api.NewInlineKeyboardMarkup(rows...)
	return text, &keyboard, nil
}

func (a *Admin) renderGatekeeperGreeting(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language

	builder := strings.Builder{}
	builder.WriteString(i18n.Get("Greeting Settings", lang))
	builder.WriteString("\n\n")
	builder.WriteString(fmt.Sprintf("%s %s\n\n", statusEmoji(state.Features.GatekeeperGreetingEnabled), i18n.Get("Greeting", lang)))
	builder.WriteString(i18n.Get("Greeting Preview", lang))
	builder.WriteString("\n")
	preview := renderGreetingPreviewQuote(state)
	if preview == "" {
		builder.WriteString("> ")
		builder.WriteString(i18n.Get("No greeting text configured", lang))
	} else {
		builder.WriteString(preview)
	}

	toggleBtn, err := a.commandButton(ctx, session.ID, fmt.Sprintf("%s %s", statusEmoji(state.Features.GatekeeperGreetingEnabled), i18n.Get("Greeting", lang)), panelCommand{Action: panelActionGatekeeperToggleGreeting})
	if err != nil {
		return "", nil, err
	}
	editBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Edit Greeting", lang), panelCommand{Action: panelActionGatekeeperEditGreeting})
	if err != nil {
		return "", nil, err
	}
	clearBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Clear Greeting", lang), panelCommand{Action: panelActionGatekeeperClearGreeting})
	if err != nil {
		return "", nil, err
	}
	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}

	keyboard := api.NewInlineKeyboardMarkup(
		api.NewInlineKeyboardRow(toggleBtn),
		api.NewInlineKeyboardRow(editBtn, clearBtn),
		api.NewInlineKeyboardRow(backBtn),
	)
	return builder.String(), &keyboard, nil
}

func (a *Admin) renderGatekeeperGreetingPrompt(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language

	builder := strings.Builder{}
	builder.WriteString(i18n.Get("Edit Greeting", lang))
	builder.WriteString("\n\n")
	if state.PromptError != "" {
		builder.WriteString(state.PromptError)
		builder.WriteString("\n\n")
	}
	builder.WriteString(i18n.Get("Send the greeting text", lang))
	builder.WriteString("\n\n")
	builder.WriteString(i18n.Get("Available placeholders:\n{user} - user mention\n{chat_title} - chat title\n{chat_link_titled} - linked chat title or plain title\n{timeout} - challenge timeout", lang))
	builder.WriteString("\n\n")
	builder.WriteString(i18n.Get("Greeting Preview", lang))
	builder.WriteString("\n")
	preview := renderGreetingPreviewQuote(state)
	if preview == "" {
		builder.WriteString("> ")
		builder.WriteString(i18n.Get("No greeting text configured", lang))
	} else {
		builder.WriteString(preview)
	}

	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}
	clearBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Clear Greeting", lang), panelCommand{Action: panelActionGatekeeperClearGreeting})
	if err != nil {
		return "", nil, err
	}
	keyboard := api.NewInlineKeyboardMarkup(api.NewInlineKeyboardRow(backBtn, clearBtn))
	return builder.String(), &keyboard, nil
}

func (a *Admin) renderLLM(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	text := fmt.Sprintf(
		"%s\n\n%s %s\n\n%s",
		i18n.Get("LLM First Message", lang),
		statusEmoji(state.Features.LLMFirstMessageEnabled),
		i18n.Get("LLM First Message", lang),
		fmt.Sprintf(i18n.Get("Prompt examples cap: %d", lang), panelLLMExamplesCap),
	)

	toggleBtn, err := a.commandButton(ctx, session.ID, fmt.Sprintf("%s %s", statusEmoji(state.Features.LLMFirstMessageEnabled), i18n.Get("LLM First Message", lang)), panelCommand{Action: panelActionToggleFeature, Feature: panelFeatureLLMFirst})
	if err != nil {
		return "", nil, err
	}
	examplesBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Spam Examples", lang), panelCommand{Action: panelActionOpenExamples})
	if err != nil {
		return "", nil, err
	}
	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}

	keyboard := api.NewInlineKeyboardMarkup(
		api.NewInlineKeyboardRow(toggleBtn),
		api.NewInlineKeyboardRow(examplesBtn),
		api.NewInlineKeyboardRow(backBtn),
	)
	return text, &keyboard, nil
}

func (a *Admin) renderExamplesList(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	totalCount, err := a.store.CountChatSpamExamples(ctx, session.ChatID)
	if err != nil {
		return "", nil, err
	}
	totalPages := pageCount(totalCount, panelExamplesPageSize)
	state.ListPage = clampPage(state.ListPage, totalPages)

	offset := state.ListPage * panelExamplesPageSize
	examples, err := a.store.ListChatSpamExamples(ctx, session.ChatID, panelExamplesPageSize, offset)
	if err != nil {
		return "", nil, err
	}

	builder := strings.Builder{}
	builder.WriteString(i18n.Get("Spam Examples", lang))
	builder.WriteString("\n\n")
	if len(examples) == 0 {
		builder.WriteString(i18n.Get("No spam examples yet", lang))
	} else {
		for i, example := range examples {
			preview := makePreview(example.Text, panelPreviewMaxLen)
			line := fmt.Sprintf("%d. %s", i+1, preview)
			builder.WriteString(line)
			builder.WriteString("\n")
		}
	}

	addBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Add Example", lang), panelCommand{Action: panelActionAddExample})
	if err != nil {
		return "", nil, err
	}

	var exampleButtons []api.InlineKeyboardButton
	for i, example := range examples {
		label := fmt.Sprintf("%d", i+1)
		btn, err := a.commandButton(ctx, session.ID, label, panelCommand{Action: panelActionSelectExample, ExampleID: example.ID})
		if err != nil {
			return "", nil, err
		}
		exampleButtons = append(exampleButtons, btn)
	}

	rows := [][]api.InlineKeyboardButton{api.NewInlineKeyboardRow(addBtn)}
	rows = append(rows, chunkButtons(exampleButtons, 2)...)
	navRow, err := a.navRow(ctx, session.ID, panelActionExamplesPagePrev, panelActionExamplesPageNext, state.ListPage > 0, state.ListPage < totalPages-1)
	if err != nil {
		return "", nil, err
	}
	rows = append(rows, navRow)

	keyboard := api.NewInlineKeyboardMarkup(rows...)
	return builder.String(), &keyboard, nil
}

func (a *Admin) renderExampleDetail(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	example, err := a.store.GetChatSpamExample(ctx, state.SelectedExampleID)
	if err != nil {
		return "", nil, err
	}
	if example == nil {
		state.Page = panelPageExamplesList
		return a.renderExamplesList(ctx, session, state)
	}

	text := fmt.Sprintf("%s\n\n%s", i18n.Get("Spam Example", lang), example.Text)
	deleteBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Delete", lang), panelCommand{Action: panelActionOpenDelete})
	if err != nil {
		return "", nil, err
	}
	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}
	keyboard := api.NewInlineKeyboardMarkup(api.NewInlineKeyboardRow(deleteBtn, backBtn))
	return text, &keyboard, nil
}

func (a *Admin) renderExamplePrompt(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	builder := strings.Builder{}
	builder.WriteString(i18n.Get("Add Spam Example", lang))
	builder.WriteString("\n\n")
	if state.PromptError != "" {
		builder.WriteString(state.PromptError)
		builder.WriteString("\n\n")
	}
	builder.WriteString(i18n.Get("Send the spam example text", lang))

	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}
	keyboard := api.NewInlineKeyboardMarkup(api.NewInlineKeyboardRow(backBtn))
	return builder.String(), &keyboard, nil
}

func (a *Admin) renderConfirmDelete(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	example, err := a.store.GetChatSpamExample(ctx, state.SelectedExampleID)
	if err != nil {
		return "", nil, err
	}
	preview := ""
	if example != nil {
		preview = makePreview(example.Text, panelPreviewMaxLen)
	}
	text := fmt.Sprintf("%s\n\n%s", i18n.Get("Delete example?", lang), preview)

	deleteBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Delete", lang), panelCommand{Action: panelActionDeleteYes})
	if err != nil {
		return "", nil, err
	}
	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionDeleteNo})
	if err != nil {
		return "", nil, err
	}
	keyboard := api.NewInlineKeyboardMarkup(api.NewInlineKeyboardRow(deleteBtn, backBtn))
	return text, &keyboard, nil
}

func (a *Admin) renderVoting(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language

	text := fmt.Sprintf(
		"%s\n\n%s %s\n%s: %s\n%s: %s\n%s: %s\n%s: %s\n\n%s\n%s",
		i18n.Get("Community Voting", lang),
		statusEmoji(state.Features.CommunityVotingEnabled),
		i18n.Get("Community Voting", lang),
		i18n.Get("Voting timeout", lang),
		panelVotingTimeoutStateLabel(lang, state.CommunityVotingTimeoutOverrideNS),
		i18n.Get("Min voters", lang),
		panelVotingIntStateLabel(lang, state.CommunityVotingMinVotersOverride, false),
		i18n.Get("Max voters", lang),
		panelVotingIntStateLabel(lang, state.CommunityVotingMaxVotersOverride, true),
		i18n.Get("Min voters %", lang),
		panelVotingIntStateLabel(lang, state.CommunityVotingMinVotersPercentOverride, false),
		i18n.Get("Voting policy", lang),
		i18n.Get("Insufficient votes on timeout => false-positive\nTie => wait one deciding vote", lang),
	)

	toggleBtn, err := a.commandButton(ctx, session.ID, fmt.Sprintf("%s %s", statusEmoji(state.Features.CommunityVotingEnabled), i18n.Get("Community Voting", lang)), panelCommand{Action: panelActionToggleFeature, Feature: panelFeatureVoting})
	if err != nil {
		return "", nil, err
	}
	timeoutBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Voting timeout", lang), panelCommand{Action: panelActionOpenVotingTimeout})
	if err != nil {
		return "", nil, err
	}
	minBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Min voters", lang), panelCommand{Action: panelActionOpenVotingMinVoters})
	if err != nil {
		return "", nil, err
	}
	maxBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Max voters", lang), panelCommand{Action: panelActionOpenVotingMaxVoters})
	if err != nil {
		return "", nil, err
	}
	percentBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Min voters %", lang), panelCommand{Action: panelActionOpenVotingMinPercent})
	if err != nil {
		return "", nil, err
	}
	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}

	keyboard := api.NewInlineKeyboardMarkup(
		api.NewInlineKeyboardRow(toggleBtn),
		api.NewInlineKeyboardRow(timeoutBtn),
		api.NewInlineKeyboardRow(minBtn),
		api.NewInlineKeyboardRow(maxBtn),
		api.NewInlineKeyboardRow(percentBtn),
		api.NewInlineKeyboardRow(backBtn),
	)
	return text, &keyboard, nil
}

func (a *Admin) renderVotingTimeout(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	text := fmt.Sprintf("%s\n\n%s: %s", i18n.Get("Voting timeout", lang), i18n.Get("Selected", lang), panelVotingTimeoutStateLabel(lang, state.CommunityVotingTimeoutOverrideNS))

	buttons := make([]api.InlineKeyboardButton, 0, len(panelVotingTimeoutOptions)+1)
	inheritBtn, err := a.commandButton(ctx, session.ID, panelSelectLabel(state.CommunityVotingTimeoutOverrideNS == int64(db.SettingsOverrideInherit), i18n.Get("Inherit", lang)), panelCommand{Action: panelActionSetVotingTimeout, Value: "inherit"})
	if err != nil {
		return "", nil, err
	}
	buttons = append(buttons, inheritBtn)

	for _, option := range panelVotingTimeoutOptions {
		selected := state.CommunityVotingTimeoutOverrideNS != int64(db.SettingsOverrideInherit) && time.Duration(state.CommunityVotingTimeoutOverrideNS) == option
		label := panelSelectLabel(selected, panelDurationLabel(option))
		btn, err := a.commandButton(ctx, session.ID, label, panelCommand{Action: panelActionSetVotingTimeout, Value: option.String()})
		if err != nil {
			return "", nil, err
		}
		buttons = append(buttons, btn)
	}
	rows := chunkButtons(buttons, 3)
	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}
	rows = append(rows, api.NewInlineKeyboardRow(backBtn))
	keyboard := api.NewInlineKeyboardMarkup(rows...)
	return text, &keyboard, nil
}

func (a *Admin) renderVotingMinVoters(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	text := fmt.Sprintf("%s\n\n%s: %s", i18n.Get("Min voters", lang), i18n.Get("Selected", lang), panelVotingIntStateLabel(lang, state.CommunityVotingMinVotersOverride, false))

	buttons := make([]api.InlineKeyboardButton, 0, len(panelVotingMinVotersOptions)+1)
	inheritBtn, err := a.commandButton(ctx, session.ID, panelSelectLabel(state.CommunityVotingMinVotersOverride == db.SettingsOverrideInherit, i18n.Get("Inherit", lang)), panelCommand{Action: panelActionSetVotingMinVoters, Value: "inherit"})
	if err != nil {
		return "", nil, err
	}
	buttons = append(buttons, inheritBtn)
	for _, option := range panelVotingMinVotersOptions {
		label := panelSelectLabel(state.CommunityVotingMinVotersOverride == option, fmt.Sprintf("%d", option))
		btn, err := a.commandButton(ctx, session.ID, label, panelCommand{Action: panelActionSetVotingMinVoters, Value: strconv.Itoa(option)})
		if err != nil {
			return "", nil, err
		}
		buttons = append(buttons, btn)
	}
	rows := chunkButtons(buttons, 3)
	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}
	rows = append(rows, api.NewInlineKeyboardRow(backBtn))
	keyboard := api.NewInlineKeyboardMarkup(rows...)
	return text, &keyboard, nil
}

func (a *Admin) renderVotingMaxVoters(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	text := fmt.Sprintf("%s\n\n%s: %s", i18n.Get("Max voters", lang), i18n.Get("Selected", lang), panelVotingIntStateLabel(lang, state.CommunityVotingMaxVotersOverride, true))

	buttons := make([]api.InlineKeyboardButton, 0, len(panelVotingMaxVotersOptions)+1)
	inheritBtn, err := a.commandButton(ctx, session.ID, panelSelectLabel(state.CommunityVotingMaxVotersOverride == db.SettingsOverrideInherit, i18n.Get("Inherit", lang)), panelCommand{Action: panelActionSetVotingMaxVoters, Value: "inherit"})
	if err != nil {
		return "", nil, err
	}
	buttons = append(buttons, inheritBtn)
	for _, option := range panelVotingMaxVotersOptions {
		valueLabel := fmt.Sprintf("%d", option)
		if option == 0 {
			valueLabel = i18n.Get("No cap", lang)
		}
		label := panelSelectLabel(state.CommunityVotingMaxVotersOverride == option, valueLabel)
		btn, err := a.commandButton(ctx, session.ID, label, panelCommand{Action: panelActionSetVotingMaxVoters, Value: strconv.Itoa(option)})
		if err != nil {
			return "", nil, err
		}
		buttons = append(buttons, btn)
	}
	rows := chunkButtons(buttons, 3)
	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}
	rows = append(rows, api.NewInlineKeyboardRow(backBtn))
	keyboard := api.NewInlineKeyboardMarkup(rows...)
	return text, &keyboard, nil
}

func (a *Admin) renderVotingMinPercent(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	text := fmt.Sprintf("%s\n\n%s: %s", i18n.Get("Min voters %", lang), i18n.Get("Selected", lang), panelVotingIntStateLabel(lang, state.CommunityVotingMinVotersPercentOverride, false))

	buttons := make([]api.InlineKeyboardButton, 0, len(panelVotingMinVotersPercentOptions)+1)
	inheritBtn, err := a.commandButton(ctx, session.ID, panelSelectLabel(state.CommunityVotingMinVotersPercentOverride == db.SettingsOverrideInherit, i18n.Get("Inherit", lang)), panelCommand{Action: panelActionSetVotingMinPercent, Value: "inherit"})
	if err != nil {
		return "", nil, err
	}
	buttons = append(buttons, inheritBtn)
	for _, option := range panelVotingMinVotersPercentOptions {
		label := panelSelectLabel(state.CommunityVotingMinVotersPercentOverride == option, fmt.Sprintf("%d%%", option))
		btn, err := a.commandButton(ctx, session.ID, label, panelCommand{Action: panelActionSetVotingMinPercent, Value: strconv.Itoa(option)})
		if err != nil {
			return "", nil, err
		}
		buttons = append(buttons, btn)
	}
	rows := chunkButtons(buttons, 3)
	backBtn, err := a.commandButton(ctx, session.ID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}
	rows = append(rows, api.NewInlineKeyboardRow(backBtn))
	keyboard := api.NewInlineKeyboardMarkup(rows...)
	return text, &keyboard, nil
}

func (a *Admin) renderConfirmClose(ctx context.Context, session *db.AdminPanelSession, state *panelState) (string, *api.InlineKeyboardMarkup, error) {
	lang := state.Language
	text := i18n.Get("Close settings panel?", lang)

	confirmBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Close", lang), panelCommand{Action: panelActionCloseConfirm})
	if err != nil {
		return "", nil, err
	}
	cancelBtn, err := a.commandButton(ctx, session.ID, i18n.Get("Cancel", lang), panelCommand{Action: panelActionBack})
	if err != nil {
		return "", nil, err
	}
	keyboard := api.NewInlineKeyboardMarkup(api.NewInlineKeyboardRow(confirmBtn, cancelBtn))
	return text, &keyboard, nil
}

func (a *Admin) commandButton(ctx context.Context, sessionID int64, label string, cmd panelCommand) (api.InlineKeyboardButton, error) {
	payload, err := a.createPanelCommand(ctx, sessionID, cmd)
	if err != nil {
		return api.InlineKeyboardButton{}, err
	}
	return api.NewInlineKeyboardButtonData(label, payload), nil
}

func (a *Admin) navRow(ctx context.Context, sessionID int64, prevAction string, nextAction string, hasPrev bool, hasNext bool) ([]api.InlineKeyboardButton, error) {
	row := make([]api.InlineKeyboardButton, 0, 3)
	if hasPrev {
		prevBtn, err := a.commandButton(ctx, sessionID, "⬅️", panelCommand{Action: prevAction})
		if err != nil {
			return nil, err
		}
		row = append(row, prevBtn)
	}
	backBtn, err := a.commandButton(ctx, sessionID, "↩️", panelCommand{Action: panelActionBack})
	if err != nil {
		return nil, err
	}
	row = append(row, backBtn)
	if hasNext {
		nextBtn, err := a.commandButton(ctx, sessionID, "➡️", panelCommand{Action: nextAction})
		if err != nil {
			return nil, err
		}
		row = append(row, nextBtn)
	}
	return api.NewInlineKeyboardRow(row...), nil
}

func (a *Admin) createPanelCommand(ctx context.Context, sessionID int64, cmd panelCommand) (string, error) {
	payload, err := jsonMarshalCommand(cmd)
	if err != nil {
		return "", err
	}
	created, err := a.store.CreateAdminPanelCommand(ctx, &db.AdminPanelCommand{
		SessionID: sessionID,
		Payload:   payload,
		CreatedAt: time.Now(),
	})
	if err != nil {
		return "", err
	}
	sessionEnc := encodeUint64Min(uint64(sessionID))
	commandEnc := encodeUint64Min(uint64(created.ID))
	return sessionEnc + "_" + commandEnc, nil
}

func jsonMarshalCommand(cmd panelCommand) (string, error) {
	data, err := json.Marshal(cmd)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func panelDurationLabel(duration time.Duration) string {
	if duration <= 0 {
		return "0s"
	}
	if duration%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(duration/time.Minute))
	}
	if duration%time.Second == 0 {
		return fmt.Sprintf("%ds", int(duration/time.Second))
	}
	return duration.String()
}

func panelVotingTimeoutStateLabel(lang string, overrideNS int64) string {
	if overrideNS == int64(db.SettingsOverrideInherit) {
		return i18n.Get("Inherit", lang)
	}
	return panelDurationLabel(time.Duration(overrideNS))
}

func panelVotingIntStateLabel(lang string, value int, zeroNoCap bool) string {
	if value == db.SettingsOverrideInherit {
		return i18n.Get("Inherit", lang)
	}
	if zeroNoCap && value == 0 {
		return i18n.Get("No cap", lang)
	}
	return fmt.Sprintf("%d", value)
}

func panelSelectLabel(selected bool, label string) string {
	if selected {
		return "✅ " + label
	}
	return label
}

func renderGreetingPreviewQuote(state *panelState) string {
	if state == nil {
		return ""
	}
	text := strings.TrimSpace(state.GatekeeperGreetingText)
	if text == "" {
		return ""
	}

	chatTitle := state.ChatTitle
	if chatTitle == "" {
		chatTitle = "Chat"
	}

	text = strings.ReplaceAll(text, panelGreetingPlaceholderUser, fmt.Sprintf("[User](tg://user?id=%d)", state.UserID))
	text = strings.ReplaceAll(text, panelGreetingPlaceholderChatTitle, chatTitle)
	text = strings.ReplaceAll(text, panelGreetingPlaceholderChatLinkTitled, chatTitle)
	text = strings.ReplaceAll(text, panelGreetingPlaceholderTimeout, panelDurationLabel(time.Duration(state.ChallengeTimeout)))

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n")
}

func chunkButtons(buttons []api.InlineKeyboardButton, perRow int) [][]api.InlineKeyboardButton {
	if len(buttons) == 0 {
		return nil
	}
	var rows [][]api.InlineKeyboardButton
	for i := 0; i < len(buttons); i += perRow {
		end := i + perRow
		if end > len(buttons) {
			end = len(buttons)
		}
		rows = append(rows, api.NewInlineKeyboardRow(buttons[i:end]...))
	}
	return rows
}

func languageLabel(code string) string {
	name := i18n.GetLanguageName(code)
	return fmt.Sprintf("%s (%s)", name, code)
}

func statusEmoji(enabled bool) string {
	if enabled {
		return "✅"
	}
	return "⬜"
}

func makePreview(text string, maxLen int) string {
	normalized := strings.ReplaceAll(text, "\n", " ")
	normalized = strings.TrimSpace(normalized)
	normalized = strings.Join(strings.Fields(normalized), " ")
	runes := []rune(normalized)
	if len(runes) <= maxLen {
		return normalized
	}
	return string(runes[:maxLen]) + "..."
}

func pageCount(total int, pageSize int) int {
	if total <= 0 {
		return 1
	}
	return (total + pageSize - 1) / pageSize
}

func clampPage(page int, totalPages int) int {
	if totalPages <= 0 {
		return 0
	}
	if page < 0 {
		return 0
	}
	if page >= totalPages {
		return totalPages - 1
	}
	return page
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
