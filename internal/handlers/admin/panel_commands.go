package handlers

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/iamwavecut/ngbot/internal/db"
	"github.com/iamwavecut/ngbot/internal/i18n"
)

func (a *Admin) applyPanelCommand(ctx context.Context, session *db.AdminPanelSession, state *panelState, command panelCommand) error {
	switch command.Action {
	case panelActionToggleFeature:
		if err := a.toggleFeature(ctx, session, state, command.Feature); err != nil {
			return err
		}
	case panelActionOpenLanguage:
		state.Page = panelPageLanguageList
		state.LanguagePage = pageForLanguage(state.Language, i18n.GetLanguagesList(), panelLanguagePageSize)
	case panelActionOpenGatekeeper:
		state.Page = panelPageGatekeeper
		state.PromptError = ""
	case panelActionOpenGatekeeperCaptcha:
		state.Page = panelPageGatekeeperCaptcha
	case panelActionOpenGatekeeperGreeting:
		state.Page = panelPageGatekeeperGreeting
	case panelActionOpenGatekeeperCaptchaOptions:
		state.Page = panelPageGatekeeperCaptchaOptions
	case panelActionOpenGatekeeperChallengeTimeout:
		state.Page = panelPageGatekeeperChallengeTimeout
	case panelActionOpenGatekeeperRejectTimeout:
		state.Page = panelPageGatekeeperRejectTimeout
	case panelActionGatekeeperToggleMaster:
		if err := a.toggleGatekeeperMaster(ctx, session, state); err != nil {
			return err
		}
	case panelActionGatekeeperToggleCaptcha:
		if err := a.toggleGatekeeperCaptcha(ctx, session, state); err != nil {
			return err
		}
	case panelActionGatekeeperToggleGreeting:
		if err := a.toggleGatekeeperGreeting(ctx, session, state); err != nil {
			return err
		}
	case panelActionGatekeeperSetCaptchaSize:
		if err := a.setGatekeeperCaptchaSize(ctx, session, state, command.Value); err != nil {
			return err
		}
	case panelActionGatekeeperSetChallengeTTL:
		if err := a.setGatekeeperChallengeTimeout(ctx, session, state, command.Value); err != nil {
			return err
		}
	case panelActionGatekeeperSetRejectTTL:
		if err := a.setGatekeeperRejectTimeout(ctx, session, state, command.Value); err != nil {
			return err
		}
	case panelActionGatekeeperEditGreeting:
		state.Page = panelPageGatekeeperGreetingPrompt
		state.PromptError = ""
	case panelActionGatekeeperClearGreeting:
		if err := a.clearGatekeeperGreeting(ctx, session, state); err != nil {
			return err
		}
	case panelActionOpenLLM:
		state.Page = panelPageLLM
	case panelActionOpenExamples:
		state.Page = panelPageExamplesList
	case panelActionOpenVoting:
		state.Page = panelPageVoting
	case panelActionOpenVotingTimeout:
		state.Page = panelPageVotingTimeout
	case panelActionOpenVotingMinVoters:
		state.Page = panelPageVotingMinVoters
	case panelActionOpenVotingMaxVoters:
		state.Page = panelPageVotingMaxVoters
	case panelActionOpenVotingMinPercent:
		state.Page = panelPageVotingMinPercent
	case panelActionSetVotingTimeout:
		if err := a.setVotingTimeoutOverride(ctx, session, state, command.Value); err != nil {
			return err
		}
	case panelActionSetVotingMinVoters:
		if err := a.setVotingMinVotersOverride(ctx, session, state, command.Value); err != nil {
			return err
		}
	case panelActionSetVotingMaxVoters:
		if err := a.setVotingMaxVotersOverride(ctx, session, state, command.Value); err != nil {
			return err
		}
	case panelActionSetVotingMinPercent:
		if err := a.setVotingMinPercentOverride(ctx, session, state, command.Value); err != nil {
			return err
		}
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
		case panelPageGatekeeper:
			state.Page = panelPageHome
		case panelPageGatekeeperCaptcha:
			state.Page = panelPageGatekeeper
		case panelPageGatekeeperCaptchaOptions:
			state.Page = panelPageGatekeeperCaptcha
		case panelPageGatekeeperChallengeTimeout:
			state.Page = panelPageGatekeeperCaptcha
		case panelPageGatekeeperRejectTimeout:
			state.Page = panelPageGatekeeperCaptcha
		case panelPageGatekeeperGreeting:
			state.Page = panelPageGatekeeper
		case panelPageGatekeeperGreetingPrompt:
			state.Page = panelPageGatekeeperGreeting
		case panelPageLLM:
			state.Page = panelPageHome
		case panelPageExamplesList:
			state.Page = panelPageLLM
		case panelPageExampleDetail:
			state.Page = panelPageExamplesList
		case panelPageExamplePrompt:
			state.Page = panelPageExamplesList
		case panelPageConfirmDelete:
			state.Page = panelPageExampleDetail
		case panelPageVoting:
			state.Page = panelPageHome
		case panelPageVotingTimeout:
			state.Page = panelPageVoting
		case panelPageVotingMinVoters:
			state.Page = panelPageVoting
		case panelPageVotingMaxVoters:
			state.Page = panelPageVoting
		case panelPageVotingMinPercent:
			state.Page = panelPageVoting
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
	syncPanelStateFromSettings(state, settings)
	return nil
}

func (a *Admin) toggleGatekeeperMaster(ctx context.Context, session *db.AdminPanelSession, state *panelState) error {
	settings, err := a.s.GetSettings(ctx, session.ChatID)
	if err != nil {
		return err
	}
	settings.GatekeeperEnabled = !settings.GatekeeperEnabled
	if err := a.s.SetSettings(ctx, settings); err != nil {
		return err
	}
	syncPanelStateFromSettings(state, settings)
	return nil
}

func (a *Admin) toggleGatekeeperCaptcha(ctx context.Context, session *db.AdminPanelSession, state *panelState) error {
	settings, err := a.s.GetSettings(ctx, session.ChatID)
	if err != nil {
		return err
	}
	settings.GatekeeperCaptchaEnabled = !settings.GatekeeperCaptchaEnabled
	if err := a.s.SetSettings(ctx, settings); err != nil {
		return err
	}
	syncPanelStateFromSettings(state, settings)
	return nil
}

func (a *Admin) toggleGatekeeperGreeting(ctx context.Context, session *db.AdminPanelSession, state *panelState) error {
	settings, err := a.s.GetSettings(ctx, session.ChatID)
	if err != nil {
		return err
	}
	settings.GatekeeperGreetingEnabled = !settings.GatekeeperGreetingEnabled
	if err := a.s.SetSettings(ctx, settings); err != nil {
		return err
	}
	syncPanelStateFromSettings(state, settings)
	return nil
}

func (a *Admin) setGatekeeperCaptchaSize(ctx context.Context, session *db.AdminPanelSession, state *panelState, value string) error {
	size, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}
	switch size {
	case 3, 4, 5, 6, 8, 10:
	default:
		return nil
	}

	settings, err := a.s.GetSettings(ctx, session.ChatID)
	if err != nil {
		return err
	}
	settings.GatekeeperCaptchaOptionsCount = size
	if err := a.s.SetSettings(ctx, settings); err != nil {
		return err
	}
	syncPanelStateFromSettings(state, settings)
	return nil
}

func (a *Admin) setGatekeeperChallengeTimeout(ctx context.Context, session *db.AdminPanelSession, state *panelState, value string) error {
	duration, err := time.ParseDuration(value)
	if err != nil || !containsDuration(panelChallengeTimeoutOptions, duration) {
		return nil
	}
	settings, err := a.s.GetSettings(ctx, session.ChatID)
	if err != nil {
		return err
	}
	settings.ChallengeTimeout = duration.Nanoseconds()
	if err := a.s.SetSettings(ctx, settings); err != nil {
		return err
	}
	syncPanelStateFromSettings(state, settings)
	return nil
}

func (a *Admin) setGatekeeperRejectTimeout(ctx context.Context, session *db.AdminPanelSession, state *panelState, value string) error {
	duration, err := time.ParseDuration(value)
	if err != nil || !containsDuration(panelRejectTimeoutOptions, duration) {
		return nil
	}
	settings, err := a.s.GetSettings(ctx, session.ChatID)
	if err != nil {
		return err
	}
	settings.RejectTimeout = duration.Nanoseconds()
	if err := a.s.SetSettings(ctx, settings); err != nil {
		return err
	}
	syncPanelStateFromSettings(state, settings)
	return nil
}

func (a *Admin) setVotingTimeoutOverride(ctx context.Context, session *db.AdminPanelSession, state *panelState, value string) error {
	settings, err := a.s.GetSettings(ctx, session.ChatID)
	if err != nil {
		return err
	}
	if value == "inherit" {
		settings.CommunityVotingTimeoutOverrideNS = int64(db.SettingsOverrideInherit)
	} else {
		duration, parseErr := time.ParseDuration(value)
		if parseErr != nil || !containsDuration(panelVotingTimeoutOptions, duration) {
			return nil
		}
		settings.CommunityVotingTimeoutOverrideNS = duration.Nanoseconds()
	}
	if err := a.s.SetSettings(ctx, settings); err != nil {
		return err
	}
	syncPanelStateFromSettings(state, settings)
	return nil
}

func (a *Admin) setVotingMinVotersOverride(ctx context.Context, session *db.AdminPanelSession, state *panelState, value string) error {
	settings, err := a.s.GetSettings(ctx, session.ChatID)
	if err != nil {
		return err
	}
	if value == "inherit" {
		settings.CommunityVotingMinVotersOverride = db.SettingsOverrideInherit
	} else {
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || !containsInt(panelVotingMinVotersOptions, parsed) {
			return nil
		}
		settings.CommunityVotingMinVotersOverride = parsed
	}
	if err := a.s.SetSettings(ctx, settings); err != nil {
		return err
	}
	syncPanelStateFromSettings(state, settings)
	return nil
}

func (a *Admin) setVotingMaxVotersOverride(ctx context.Context, session *db.AdminPanelSession, state *panelState, value string) error {
	settings, err := a.s.GetSettings(ctx, session.ChatID)
	if err != nil {
		return err
	}
	if value == "inherit" {
		settings.CommunityVotingMaxVotersOverride = db.SettingsOverrideInherit
	} else {
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || !containsInt(panelVotingMaxVotersOptions, parsed) {
			return nil
		}
		settings.CommunityVotingMaxVotersOverride = parsed
	}
	if err := a.s.SetSettings(ctx, settings); err != nil {
		return err
	}
	syncPanelStateFromSettings(state, settings)
	return nil
}

func (a *Admin) setVotingMinPercentOverride(ctx context.Context, session *db.AdminPanelSession, state *panelState, value string) error {
	settings, err := a.s.GetSettings(ctx, session.ChatID)
	if err != nil {
		return err
	}
	if value == "inherit" {
		settings.CommunityVotingMinVotersPercentOverride = db.SettingsOverrideInherit
	} else {
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || !containsInt(panelVotingMinVotersPercentOptions, parsed) {
			return nil
		}
		settings.CommunityVotingMinVotersPercentOverride = parsed
	}
	if err := a.s.SetSettings(ctx, settings); err != nil {
		return err
	}
	syncPanelStateFromSettings(state, settings)
	return nil
}

func (a *Admin) clearGatekeeperGreeting(ctx context.Context, session *db.AdminPanelSession, state *panelState) error {
	settings, err := a.s.GetSettings(ctx, session.ChatID)
	if err != nil {
		return err
	}
	settings.GatekeeperGreetingText = ""
	if err := a.s.SetSettings(ctx, settings); err != nil {
		return err
	}
	syncPanelStateFromSettings(state, settings)
	state.PromptError = ""
	return nil
}

func syncPanelStateFromSettings(state *panelState, settings *db.Settings) {
	if state == nil || settings == nil {
		return
	}
	state.Features = panelFeatureFlags{
		GatekeeperEnabled:         settings.GatekeeperEnabled,
		GatekeeperCaptchaEnabled:  settings.GatekeeperCaptchaEnabled,
		GatekeeperGreetingEnabled: settings.GatekeeperGreetingEnabled,
		GatekeeperEffective:       settings.GatekeeperEnabled && (settings.GatekeeperCaptchaEnabled || settings.GatekeeperGreetingEnabled),
		LLMFirstMessageEnabled:    settings.LLMFirstMessageEnabled,
		CommunityVotingEnabled:    settings.CommunityVotingEnabled,
	}
	state.GatekeeperCaptchaOptionsCount = settings.GatekeeperCaptchaOptionsCount
	state.GatekeeperGreetingText = settings.GatekeeperGreetingText
	state.CommunityVotingTimeoutOverrideNS = settings.CommunityVotingTimeoutOverrideNS
	state.CommunityVotingMinVotersOverride = settings.CommunityVotingMinVotersOverride
	state.CommunityVotingMaxVotersOverride = settings.CommunityVotingMaxVotersOverride
	state.CommunityVotingMinVotersPercentOverride = settings.CommunityVotingMinVotersPercentOverride
	state.ChallengeTimeout = settings.ChallengeTimeout
	state.RejectTimeout = settings.RejectTimeout
	state.Language = settings.Language
}

func containsDuration(candidates []time.Duration, value time.Duration) bool {
	return slices.Contains(candidates, value)
}

func containsInt(candidates []int, value int) bool {
	return slices.Contains(candidates, value)
}

func (a *Admin) setChatLanguage(ctx context.Context, chatID int64, language string) error {
	settings, err := a.s.GetSettings(ctx, chatID)
	if err != nil {
		return err
	}
	settings.Language = language
	return a.s.SetSettings(ctx, settings)
}
