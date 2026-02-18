package handlers

import (
	"context"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

func (a *Admin) applyPanelCommand(ctx context.Context, session *db.AdminPanelSession, state *panelState, command panelCommand) error {
	switch command.Action {
	case panelActionToggleFeature:
		return a.toggleFeature(ctx, session, state, command.Feature)
	case panelActionOpenLanguage:
		state.Page = panelPageLanguageList
		state.LanguagePage = pageForLanguage(state.Language, i18n.GetLanguagesList(), panelLanguagePageSize)
	case panelActionLanguagePageNext:
		state.LanguagePage++
	case panelActionLanguagePagePrev:
		if state.LanguagePage > 0 {
			state.LanguagePage--
		}
	case panelActionSelectLanguage:
		if err := a.setChatLanguage(ctx, session.ChatID, command.Language); err != nil {
			return err
		}
		state.Language = command.Language
		state.Page = panelPageHome
	case panelActionOpenExamples:
		state.Page = panelPageExamplesList
	case panelActionExamplesPageNext:
		state.ListPage++
	case panelActionExamplesPagePrev:
		if state.ListPage > 0 {
			state.ListPage--
		}
	case panelActionAddExample:
		state.Page = panelPageExamplePrompt
		state.PromptError = ""
	case panelActionSelectExample:
		state.Page = panelPageExampleDetail
		state.SelectedExampleID = command.ExampleID
	case panelActionOpenDelete:
		state.Page = panelPageConfirmDelete
	case panelActionDeleteYes:
		if state.SelectedExampleID != 0 {
			if err := a.store.DeleteChatSpamExample(ctx, state.SelectedExampleID); err != nil {
				return err
			}
		}
		state.Page = panelPageExamplesList
		state.SelectedExampleID = 0
	case panelActionDeleteNo:
		state.Page = panelPageExampleDetail
	case panelActionBack:
		switch state.Page {
		case panelPageLanguageList:
			state.Page = panelPageHome
		case panelPageExamplesList:
			state.Page = panelPageHome
		case panelPageExampleDetail:
			state.Page = panelPageExamplesList
		case panelPageExamplePrompt:
			state.Page = panelPageExamplesList
		case panelPageConfirmDelete:
			state.Page = panelPageExampleDetail
		case panelPageConfirmClose:
			state.Page = panelPageHome
		default:
			state.Page = panelPageHome
		}
	case panelActionClose:
		state.Page = panelPageConfirmClose
	default:
	}
	return a.savePanelState(ctx, session, *state)
}

func (a *Admin) toggleFeature(ctx context.Context, session *db.AdminPanelSession, state *panelState, feature string) error {
	settings, err := a.s.GetSettings(ctx, session.ChatID)
	if err != nil {
		return err
	}
	switch feature {
	case panelFeatureGatekeeper:
		settings.GatekeeperEnabled = !settings.GatekeeperEnabled
	case panelFeatureLLMFirst:
		settings.LLMFirstMessageEnabled = !settings.LLMFirstMessageEnabled
	case panelFeatureVoting:
		settings.CommunityVotingEnabled = !settings.CommunityVotingEnabled
	default:
		return nil
	}
	if err := a.s.SetSettings(ctx, settings); err != nil {
		return err
	}
	state.Features = panelFeatureFlags{
		GatekeeperEnabled:      settings.GatekeeperEnabled,
		LLMFirstMessageEnabled: settings.LLMFirstMessageEnabled,
		CommunityVotingEnabled: settings.CommunityVotingEnabled,
	}
	return a.savePanelState(ctx, session, *state)
}

func (a *Admin) setChatLanguage(ctx context.Context, chatID int64, language string) error {
	settings, err := a.s.GetSettings(ctx, chatID)
	if err != nil {
		return err
	}
	settings.Language = language
	return a.s.SetSettings(ctx, settings)
}
