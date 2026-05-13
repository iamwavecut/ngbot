# Аудит бизнес-логики ngbot

Дата: 2026-05-05

## Резюме

Аудит сфокусирован на местах, где пользовательское действие может привести к бану, муту, удалению сообщения, изменению защитных настроек или обходу антиспама. Обновление 2026-05-13: user-facing `/ban` заменен на `/voteban` / reply `@bot`, репорты проходят повторную LLM-проверку и message-bound voting без предварительного удаления сообщения.

| Severity | Статус | Поверхность | Риск |
| --- | --- | --- | --- |
| P0 | Исправлено | `/voteban` / `@bot` | Обычный пользователь мог вызвать старый путь, который удалял сообщение до голосования |
| P1 | Частично исправлено | `spam_vote` callbacks | Suspect user больше не может голосовать в собственном кейсе; non-member голоса остаются допустимыми для linked public channel comments |
| P1 | Открыто | Admin panel | Админ без ban/kick-права может менять защитные настройки и allowlist |
| P2 | Исправлено | Spam cases | Report-first кейсы привязаны к конкретному `message_id` и хранят report-message артефакты |
| P2 | Открыто | Not-spammer overrides | Allowlist проверяется раньше внешнего banlist/LLM и может стать широким обходом |
| P2 | Открыто | Gatekeeper handoff/timeout | Логика стала status-aware, но остается критичной зоной для регрессий |

## Находки

| Severity | Поверхность | Exploit path | Текущий guard | Недостающий guard | Рекомендация | Regression test |
| --- | --- | --- | --- | --- | --- | --- |
| P0 | `/voteban` / `@bot` report | Обычный пользователь отвечает на сообщение старой командой и запускает удаление до голосования | User-facing `/ban` игнорируется; `/voteban` и reply `@bot` сначала запускают report-specific LLM check | Нет открытого P0 guard gap для этого пути | Исправлено: LLM-confirmed spam идет в existing ban/delete path, иначе non-restrict reporter создает voting без pre-delete/pre-mute | `TestVoteBanCommandRoutesByRestrictPermissionAfterReportCheck`, `TestProcessReportedMessageStartsVotingWithoutDeletingOrMuting` |
| P1 | Community voting callback `spam_vote` | Suspect user получает inline-кнопку голосования и голосует в собственном кейсе | Проверяются формат callback, наличие spam case и `CommunityVotingEnabled` для `spamCase.ChatID`; `RecordVote` запрещает `spamCase.UserID == voterID` | Для продукта linked public channel comments non-member voters are allowed, поэтому membership не является guard'ом | Оставить self-vote запрет; не вводить member-only guard без отдельного продуктового решения | Callback от suspect получает отказ |
| P1 | Admin panel settings/allowlist | Админ без ban/kick-права, но с `CanManageChat` или `CanPromoteMembers`, открывает `/settings`, выключает voting/gatekeeper/LLM или добавляет not-spammer override | `ensureManagerAccess` использует `permissions.IsManager`; это продуктово шире, чем moderation privilege | Разделение прав: general manager для чтения/части настроек, restrict-capable admin/creator для security-critical actions | Ввести per-action permission policy: изменения gatekeeper, LLM first message, voting, spam examples, not-spammer overrides требуют `CanRestrictMembers` или creator | Manager-only может открыть read-only/limited panel, но не может менять защитные toggles и allowlist |
| P2 | Active spam case binding | На пользователя уже есть pending spam case; новое сообщение того же user в том же chat повторно использует старый case и может не создать новый вопрос/ссылку на новое сообщение | Report-first path uses `GetActiveSpamCaseByMessage(chatID,userID,messageID)` and stores `spam_case_report_messages` | Legacy non-report quarantine path still keeps per-user reuse semantics | Для future cleanup можно разделить legacy quarantine и report case модели еще явнее | Repeated reports of same message reuse one case; different report target can create a distinct message-bound case |
| P2 | Not-spammer overrides | Username/userID попадает в allowlist; после этого bypass происходит до external banlist и LLM, включая global rows, которые можно вставить только напрямую в DB | Admin panel требует manager access; lookup поддерживает chat-scoped priority и global scope | Ограничение силы override относительно известных external banned users и аудит источника override | Разделить allowlist на chat-local и emergency/global; external known-banned users не должны обходиться без creator/restrict-admin подтверждения | Global override не обходит known-banned без явного high-privilege approval |
| P2 | Gatekeeper handoff/timeout | Регрессия в порядке `chat_member` handoff lookup может снова привести к restrict/ban после успешной DM CAPTCHA; timeout ветки удаляют/банят в нескольких местах | Сейчас есть `passed_waiting_member_join`, `GetPassedJoinRequestChallengeByChatUser`, no-penalty cleanup for handoff/DM states | Набор инвариантов на все state transitions и side effects | Оставить текущую логику, но усилить тест-матрицу: каждый статус challenge должен иметь ровно ожидаемые Telegram side effects | Для каждого challenge status test asserts no unexpected `restrictChatMember`, `banChatMember`, `declineChatJoinRequest` |
| P2 | Direct moderation side effects | `ProcessBannedMessage`, `ResolveCase`, gatekeeper scheduler и reaction profile check вызывают `banChatMember` напрямую через helper; часть ошибок только логируется | Telegram API error handling и `ErrNoPrivileges` в части BanService paths | Единая policy/side-effect оболочка с предсказуемой ошибкой и audit trail | Позже вынести ban/restrict/delete в общий moderation action service с caller/reason и typed errors | При `CHAT_ADMIN_REQUIRED` каждое место возвращает/логирует typed outcome и не оставляет неоднозначный статус кейса |

## Рекомендуемый порядок исправлений

1. Разделить admin panel permissions на read/general settings и security-critical actions.
2. Продолжить аудит direct moderation side effects и typed outcomes для `CHAT_ADMIN_REQUIRED`.
3. После каждого исправления запускать как минимум `go test ./internal/handlers/chat ./internal/handlers/moderation`, затем `go test ./...` и `go vet ./...`.
