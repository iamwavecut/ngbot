package handlers

type panelPage string

const (
	panelPageHome          panelPage = "Home"
	panelPageLanguageList  panelPage = "LanguageList"
	panelPageExamplesList  panelPage = "ExamplesList"
	panelPageExampleDetail panelPage = "ExampleDetail"
	panelPageExamplePrompt panelPage = "ExamplePrompt"
	panelPageConfirmDelete panelPage = "ConfirmDelete"
)

const (
	panelActionToggleFeature    = "toggle_feature"
	panelActionOpenLanguage     = "open_language"
	panelActionLanguagePageNext = "language_page_next"
	panelActionLanguagePagePrev = "language_page_prev"
	panelActionSelectLanguage   = "select_language"
	panelActionOpenExamples     = "open_examples"
	panelActionExamplesPageNext = "examples_page_next"
	panelActionExamplesPagePrev = "examples_page_prev"
	panelActionAddExample       = "add_example"
	panelActionSelectExample    = "select_example"
	panelActionOpenDelete       = "open_delete"
	panelActionDeleteYes        = "delete_yes"
	panelActionDeleteNo         = "delete_no"
	panelActionBack             = "back"
	panelActionClose            = "close"
	panelActionNoop             = "noop"
)

const (
	panelFeatureGatekeeper = "gatekeeper"
	panelFeatureLLMFirst   = "llm_first_message"
	panelFeatureVoting     = "community_voting"
)

type panelFeatureFlags struct {
	GatekeeperEnabled      bool `json:"gatekeeper_enabled"`
	LLMFirstMessageEnabled bool `json:"llm_first_message_enabled"`
	CommunityVotingEnabled bool `json:"community_voting_enabled"`
}

type panelState struct {
	SessionID         int64             `json:"session_id"`
	Page              panelPage         `json:"page"`
	ChatID            int64             `json:"chat_id"`
	UserID            int64             `json:"user_id"`
	ChatTitle         string            `json:"chat_title"`
	Language          string            `json:"language"`
	Features          panelFeatureFlags `json:"features"`
	ListPage          int               `json:"list_page"`
	LanguagePage      int               `json:"language_page"`
	SelectedExampleID int64             `json:"selected_example_id,omitempty"`
	PromptError       string            `json:"prompt_error,omitempty"`
}

type panelCommand struct {
	Action    string `json:"action"`
	Feature   string `json:"feature,omitempty"`
	ExampleID int64  `json:"example_id,omitempty"`
	Language  string `json:"language,omitempty"`
}
