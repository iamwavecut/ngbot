package handlers

import (
	"os"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/adapters/llm/gemini"
	"github.com/iamwavecut/ngbot/internal/db"
	log "github.com/sirupsen/logrus"
)

func TestLiveGeminiContextAwareModerationBoundary(t *testing.T) {
	if os.Getenv("NGBOT_RUN_LIVE_GEMINI_MODERATION") != "1" {
		t.Skip("set NGBOT_RUN_LIVE_GEMINI_MODERATION=1 to run the live semantic check")
	}

	apiKey := os.Getenv("NG_LLM_GEMINI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("NG_LLM_API_KEY")
	}
	provider, err := gemini.NewGemini(apiKey, os.Getenv("NG_LLM_API_MODEL"), log.New().WithField("test", "live_jobs_hr_boundary"))
	if err != nil {
		t.Fatalf("create Gemini adapter: %v", err)
	}
	detector := NewSpamDetector(provider, log.New().WithField("test", "live_jobs_hr_boundary"), time.Minute)
	tests := []struct {
		name    string
		message string
		spam    bool
	}{
		{
			name: "detailed project manager vacancy with recruiter contact",
			message: `#вакансия #projectmanager #middle

Middle Project Manager

Ищем PM, который любит технический контекст и хочет влиять на delivery.

Что предстоит делать:
- Вести технические проекты от постановки до релиза и post-release контроля.
- Координировать разработчиков и QA, синхронизировать релизы с продактом.
- Декомпозировать задачи, управлять сроками, рисками и блокерами.

Мы ожидаем 2–3+ года опыта, понимание жизненного цикла разработки, REST API, HTTP, БД, Git и CI/CD.

Мы предлагаем удалённую работу, отпуск, оплачиваемые sick days, обучение и компенсацию спорта.

Заинтересовала вакансия? Напишите «Привет» рекрутеру @recruiter`,
		},
		{
			name: "detailed igaming retention vacancy",
			message: `#vacancy #job #CRM #fullremote

Retention Manager в международный iGaming проект

Что предстоит делать:
- Разрабатывать Customer Journey в Email, Push, SMS и In-app.
- Работать с промо-планом, сегментацией и A/B тестами.
- Анализировать Open Rate, CTR, Churn и LTV.

Важно: от 2 лет опыта CRM или Retention Manager, разговорный English, понимание бонусных механик и базовые HTML/CSS.

Предлагаем 100% remote, удобный формат выплат, сильную команду и возможности роста.

Отклики и вопросы: Telegram @recruiter`,
		},
		{
			name:    "vague remote income solicitation",
			message: "Возьму 2-3 человека на удалённую работу. Доход 500 долларов в день. Кому интересно — пишите в личку.",
			spam:    true,
		},
	}

	for _, profile := range []string{db.LLMModerationProfileGeneral, db.LLMModerationProfileJobsHR} {
		t.Run(profile, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					result, err := detector.IsSpam(t.Context(), tt.message, ClassificationContext{Profile: profile})
					if err != nil {
						t.Fatalf("classify message: %v", err)
					}
					if result == nil || *result != tt.spam {
						t.Fatalf("spam classification = %v, want %t", result, tt.spam)
					}
				})
			}
		})
	}
}
