package handlers

type panelPage string

const (
	panelPageHome                     panelPage = "Home"
	panelPageLanguageList             panelPage = "LanguageList"
	panelPageGatekeeper               panelPage = "Gatekeeper"
	panelPageGatekeeperGreetingPrompt panelPage = "GatekeeperGreetingPrompt"
	panelPageExamplesList             panelPage = "ExamplesList"
	panelPageExampleDetail            panelPage = "ExampleDetail"
	panelPageExamplePrompt            panelPage = "ExamplePrompt"
	panelPageConfirmDelete            panelPage = "ConfirmDelete"
	panelPageConfirmClose             panelPage = "ConfirmClose"
)

const (
	panelActionToggleFeature             = "toggle_feature"
	panelActionOpenLanguage              = "open_language"
	panelActionOpenGatekeeper            = "open_gatekeeper"
	panelActionGatekeeperToggleMaster    = "gatekeeper_toggle_master"
	panelActionGatekeeperToggleCaptcha   = "gatekeeper_toggle_captcha"
	panelActionGatekeeperToggleGreeting  = "gatekeeper_toggle_greeting"
	panelActionGatekeeperSetCaptchaSize  = "gatekeeper_set_captcha_size"
	panelActionGatekeeperSetChallengeTTL = "gatekeeper_set_challenge_timeout"
	panelActionGatekeeperSetRejectTTL    = "gatekeeper_set_reject_timeout"
	panelActionGatekeeperEditGreeting    = "gatekeeper_edit_greeting"
	panelActionGatekeeperClearGreeting   = "gatekeeper_clear_greeting"
	panelActionLanguagePageNext          = "language_page_next"
	panelActionLanguagePagePrev          = "language_page_prev"
	panelActionSelectLanguage            = "select_language"
	panelActionOpenExamples              = "open_examples"
	panelActionExamplesPageNext          = "examples_page_next"
	panelActionExamplesPagePrev          = "examples_page_prev"
	panelActionAddExample                = "add_example"
	panelActionSelectExample             = "select_example"
	panelActionOpenDelete                = "open_delete"
	panelActionDeleteYes                 = "delete_yes"
	panelActionDeleteNo                  = "delete_no"
	panelActionCloseConfirm              = "close_confirm"
	panelActionBack                      = "back"
	panelActionClose                     = "close"
)

const (
	panelFeatureGatekeeper = "gatekeeper"
	panelFeatureLLMFirst   = "llm_first_message"
	panelFeatureVoting     = "community_voting"
)

type panelFeatureFlags struct {
	GatekeeperEnabled         bool `json:"gatekeeper_enabled"`
	GatekeeperCaptchaEnabled  bool `json:"gatekeeper_captcha_enabled"`
	GatekeeperGreetingEnabled bool `json:"gatekeeper_greeting_enabled"`
	GatekeeperEffective       bool `json:"gatekeeper_effective"`
	LLMFirstMessageEnabled    bool `json:"llm_first_message_enabled"`
	CommunityVotingEnabled    bool `json:"community_voting_enabled"`
}

type panelState struct {
	SessionID                     int64             `json:"session_id"`
	Page                          panelPage         `json:"page"`
	ChatID                        int64             `json:"chat_id"`
	UserID                        int64             `json:"user_id"`
	ChatTitle                     string            `json:"chat_title"`
	Language                      string            `json:"language"`
	Features                      panelFeatureFlags `json:"features"`
	GatekeeperCaptchaOptionsCount int               `json:"gatekeeper_captcha_options_count"`
	GatekeeperGreetingText        string            `json:"gatekeeper_greeting_text"`
	ChallengeTimeout              int64             `json:"challenge_timeout"`
	RejectTimeout                 int64             `json:"reject_timeout"`
	ListPage                      int               `json:"list_page"`
	LanguagePage                  int               `json:"language_page"`
	SelectedExampleID             int64             `json:"selected_example_id,omitempty"`
	PromptError                   string            `json:"prompt_error,omitempty"`
}

type panelCommand struct {
	Action    string `json:"action"`
	Feature   string `json:"feature,omitempty"`
	ExampleID int64  `json:"example_id,omitempty"`
	Language  string `json:"language,omitempty"`
	Value     string `json:"value,omitempty"`
}
