package handlers

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/iamwavecut/ngbot/internal/adapters/llm/openrouter"

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

func TestLiveOpenRouterPublicComments(t *testing.T) {
	if os.Getenv("NGBOT_RUN_LIVE_OPENROUTER_MODERATION") != "1" {
		t.Skip("set NGBOT_RUN_LIVE_OPENROUTER_MODERATION=1 to run the live semantic check")
	}
	logger := log.New()
	logger.SetFormatter(&log.JSONFormatter{})
	provider, err := openrouter.NewOpenRouter(os.Getenv("NG_LLM_OPENROUTER_API_KEY"), "", log.NewEntry(logger))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.(*openrouter.API).Close() }()
	detector := NewSpamDetector(provider, log.NewEntry(logger), 45*time.Second)
	const newsPost = "Новости"
	cases := []struct {
		name, message, post string
		spam                bool
	}{
		{"company criticism", "Anthropic опять банит обычных пользователей. OpenAI так не делает.", "Новости о блокировках Anthropic", false},
		{"ban complaint", "Мне тоже прилетел бан, просто запустил агент на большом проекте.", "Блокировки пользователей Claude", false},
		{"free sample joke", "Первая доза бесплатно закончилась.", "Gemini повысил цены", false},
		{"news source", "Вот источник новости: https://example.org/news/123456?utm_source=telegram", "Новости технологий", false},
		{"knowledge base ref", "Ответ в документации: https://docs.example.org/kb/ref/12345", "Обсуждение API", false},
		{"ordinary profile", "Это автор статьи: https://t.me/researcher", "Обсуждение исследования", false},
		{"instagram profile", "Автор видео https://instagram.com/researcher", "Обсуждение видео", false},
		{"technical start docs", "Пример deep link из документации: https://t.me/sample_bot?start=123456. Здесь start передаёт аргумент обработчику.", "Как работают deep links Telegram", false},
		{"document bot start", "Отчёт, который ты просил, открывается здесь: https://t.me/library_bot?start=article_145. Это конкретная статья с результатами эксперимента.", "Где результаты эксперимента?", false},
		{"scam warning", "Не переходите по этой мошеннической рефералке: https://fake.example/register?ref=123456", "Разбор мошенничества", false},
		{"quoted spam", "Вот как выглядит спам: «Заработок 500 долларов в день, пишите в личку». Как распознать такие схемы?", "Обсуждение спама", false},
		{"useful recommendation", "Для этой задачи попробуй поиск в https://docs.example.org, там описано API.", "Где документация?", false},
		{"personal subscription experience", "Пользуюсь платной подпиской уже месяц, цена 20 долларов.", "Сравнение тарифов моделей", false},
		{"noncommercial contact", "Напиши мне в личку, пришлю настройки для этой модели.", "Настройки локальных моделей", false},
		{"detailed hiring", "Ищем Go-разработчика: писать HTTP API, SQL-запросы и тесты. Требуется 3 года опыта Go. Удалённо, зарплата 3000 EUR. Рекрутер @recruiter.", "Вакансии", false},
		{"igaming hiring", "Retention Manager в iGaming: сегментация CRM, email-кампании, A/B-тесты. Нужен опыт CRM от 2 лет. Контакт @recruiter.", "Вакансии", false},
		{"income without explicit dm", "Есть места для желающих дополнительно зарабатывать. От 500 долларов в день, всего 2 часа с телефоном. Опыт не нужен.", "Новости моделей", true},
		{"vague recruiting", "Нужны люди для удалённой занятости, подробности у меня.", newsPost, true},
		{"referral on unknown domain", "https://unknown.example/subscribe?ref=908716", newsPost, true},
		{"referral in path", "Забирайте бонус https://unknown.example/invite/user908716", newsPost, true},
		{"opaque bot start", "https://t.me/rewards_bot?start=908716", newsPost, true},
		{"bot startapp", "https://t.me/rewards_bot/app?startapp=ref_908716", newsPost, true},
		{"promotional public channel", "Подписывайтесь на мой канал, все секреты там: https://t.me/promo_channel", newsPost, true},
		{"promotional bot", "Попробуйте нашего бесплатного бота прямо сейчас: https://t.me/promo_bot", newsPost, true},
		{"bare casino link", "https://casino.example/signup", newsPost, true},
		{"bare crypto exchange", "https://crypto-exchange.example/", newsPost, true},
		{"casino advertisement", "Зарегистрируйся в казино и забери бонус: https://casino.example/signup", newsPost, true},
		{"commercial signup", "Оформляйте подписку на AI-сервис со скидкой: https://service.example/subscribe?ic=908716", "Новости моделей", true},
		{"deanonymization advertisement", "Рекомендую сервис пробивания чужих телефонов и личных данных, подробности у @seller.", newsPost, true},
		{"safe prefix with income", "Жду новую модель. А пока нужны люди для дохода 1000 долларов в неделю, подробности в ЛС.", "Модели", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := detector.IsSpam(t.Context(), tc.message, ClassificationContext{Conversation: []ConversationMessage{{Role: "original_post", Message: tc.post}}})
			if err != nil {
				t.Fatal(err)
			}
			if result == nil || *result != tc.spam {
				t.Fatalf("classification=%v want=%t", result, tc.spam)
			}
		})
	}
	for _, profile := range []string{db.LLMModerationProfileGeneral, db.LLMModerationProfileJobsHR} {
		for _, index := range []int{14, 15, 16} {
			tc := cases[index]
			t.Run(profile+"/"+tc.name, func(t *testing.T) {
				result, err := detector.IsSpam(t.Context(), tc.message, ClassificationContext{Profile: profile})
				if err != nil || result == nil || *result != tc.spam {
					t.Fatalf("classification=%v error=%v want=%t", result, err, tc.spam)
				}
			})
		}
	}
	for i := range 3 {
		t.Run(fmt.Sprintf("cache reuse %d", i), func(t *testing.T) {
			result, err := detector.IsSpam(t.Context(), "Спасибо, теперь понятно!", ClassificationContext{})
			if err != nil || result == nil || *result {
				t.Fatalf("classification=%v error=%v", result, err)
			}
		})
	}
}

func TestLiveOpenRouterExistingExamples(t *testing.T) {
	if os.Getenv("NGBOT_RUN_LIVE_OPENROUTER_MODERATION") != "1" {
		t.Skip("live semantic check is disabled")
	}
	logger := log.New()
	logger.SetFormatter(&log.JSONFormatter{})
	provider, err := openrouter.NewOpenRouter(os.Getenv("NG_LLM_OPENROUTER_API_KEY"), "", log.NewEntry(logger))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.(*openrouter.API).Close() }()
	detector := NewSpamDetector(provider, log.NewEntry(logger), 45*time.Second)
	for index, example := range examples {
		t.Run(fmt.Sprintf("existing example %d", index), func(t *testing.T) {
			result, err := detector.IsSpam(t.Context(), example.Message, ClassificationContext{})
			if err != nil || result == nil || *result != (example.Response == 1) {
				t.Fatalf("classification=%v error=%v want=%d", result, err, example.Response)
			}
		})
	}
}
