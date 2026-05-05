# Аудит бизнес-логики ngbot

Дата: 2026-05-05

## Резюме

Аудит сфокусирован на местах, где пользовательское действие может привести к бану, муту, удалению сообщения, изменению защитных настроек или обходу антиспама. В этом проходе исправлена только уязвимость `/ban`; остальные пункты ниже являются отчетом для отдельного согласованного исправления.

| Severity | Статус | Поверхность | Риск |
| --- | --- | --- | --- |
| P0 | Исправлено | `/ban` | Мгновенный бан был доступен админам без `CanRestrictMembers` |
| P1 | Открыто | `spam_vote` callbacks | Голосовать может неавторизованный пользователь, если получил callback-кнопку |
| P1 | Открыто | Admin panel | Админ без ban/kick-права может менять защитные настройки и allowlist |
| P1 | Открыто | Reaction moderation | 5 aggregate reactions запускают авто-бан без проверки состава голосующих |
| P2 | Открыто | Spam cases | Активный кейс привязан к chat/user, а не к конкретному message_id |
| P2 | Открыто | Not-spammer overrides | Allowlist проверяется раньше внешнего banlist/LLM и может стать широким обходом |
| P2 | Открыто | Gatekeeper handoff/timeout | Логика стала status-aware, но остается критичной зоной для регрессий |

## Находки

| Severity | Поверхность | Exploit path | Текущий guard | Недостающий guard | Рекомендация | Regression test |
| --- | --- | --- | --- | --- | --- | --- |
| P0 | `/ban` command | Админ с `CanManageChat` или `CanPromoteMembers`, но без `CanRestrictMembers`, отвечает `/ban` на сообщение и получает мгновенный бан цели | Был `permissions.IsPrivilegedModerator`, который включал broad manager права | Строгая проверка creator OR admin with `CanRestrictMembers` | Исправлено: `/ban` использует `permissions.CanRestrictMembers`; остальные вызывающие `IsPrivilegedModerator` не тронуты | `TestBanCommandRoutesByRestrictPermission`, `TestCanRestrictMembers` |
| P1 | Community voting callback `spam_vote` | Пользователь получает inline-кнопку голосования в группе или лог-канале и голосует, даже если не является участником защищаемого чата или является целью кейса | Проверяются формат callback, наличие spam case и `CommunityVotingEnabled` для `spamCase.ChatID` | Проверка права голосовать в `spamCase.ChatID`: текущий member/admin, не kicked/left, желательно не сам suspect | Перед `RecordVote` загрузить spam case, проверить `GetChatMember` или `IsMember` именно в целевом чате, запретить голос suspect user; для log-channel голосов не доверять chat callback как целевому | Callback от left/non-member не добавляет `spam_votes`; callback от suspect получает отказ |
| P1 | Admin panel settings/allowlist | Админ без ban/kick-права, но с `CanManageChat` или `CanPromoteMembers`, открывает `/settings`, выключает voting/gatekeeper/LLM или добавляет not-spammer override | `ensureManagerAccess` использует `permissions.IsManager`; это продуктово шире, чем moderation privilege | Разделение прав: general manager для чтения/части настроек, restrict-capable admin/creator для security-critical actions | Ввести per-action permission policy: изменения gatekeeper, LLM first message, voting, spam examples, not-spammer overrides требуют `CanRestrictMembers` или creator | Manager-only может открыть read-only/limited panel, но не может менять защитные toggles и allowlist |
| P1 | Reaction auto-ban | Пять flagged reactions на сохраненное сообщение вызывают `banChatMember`; код не знает, кто реагировал, и не проверяет membership/дубликаты/доверенность голосов | Только aggregate `MessageReactionCountUpdated.TotalCount >= 5` и наличие автора в `lastResults` | Политика доверенных голосующих или перевод реакции в обычный spam case/voting | Снизить blast radius: вместо immediate ban создавать spam case, либо проверять voters через `MessageReactionUpdated` state; threshold сделать настройкой | Aggregate reaction count не вызывает `banChatMember` без подтвержденных eligible voters |
| P2 | Active spam case binding | На пользователя уже есть pending spam case; новое сообщение того же user в том же chat повторно использует старый case и может не создать новый вопрос/ссылку на новое сообщение | `GetActiveSpamCase(chatID,userID)` возвращает последний pending case | Привязка кейса к конкретному `message_id` или явное обновление notification target | Добавить `message_id` в `spam_cases` или отдельный message-target слой; `/ban` должен всегда создавать/обновлять case для reply target | Два разных сообщения одного user создают разные voting targets или второй `/ban` обновляет target predictably |
| P2 | Not-spammer overrides | Username/userID попадает в allowlist; после этого bypass происходит до external banlist и LLM, включая global rows, которые можно вставить только напрямую в DB | Admin panel требует manager access; lookup поддерживает chat-scoped priority и global scope | Ограничение силы override относительно известных external banned users и аудит источника override | Разделить allowlist на chat-local и emergency/global; external known-banned users не должны обходиться без creator/restrict-admin подтверждения | Global override не обходит known-banned без явного high-privilege approval |
| P2 | Gatekeeper handoff/timeout | Регрессия в порядке `chat_member` handoff lookup может снова привести к restrict/ban после успешной DM CAPTCHA; timeout ветки удаляют/банят в нескольких местах | Сейчас есть `passed_waiting_member_join`, `GetPassedJoinRequestChallengeByChatUser`, no-penalty cleanup for handoff/DM states | Набор инвариантов на все state transitions и side effects | Оставить текущую логику, но усилить тест-матрицу: каждый статус challenge должен иметь ровно ожидаемые Telegram side effects | Для каждого challenge status test asserts no unexpected `restrictChatMember`, `banChatMember`, `declineChatJoinRequest` |
| P2 | Direct moderation side effects | `ProcessBannedMessage`, `ResolveCase`, gatekeeper scheduler и reaction moderation вызывают `banChatMember` напрямую через helper; часть ошибок только логируется | Telegram API error handling и `ErrNoPrivileges` в части BanService paths | Единая policy/side-effect оболочка с предсказуемой ошибкой и audit trail | Позже вынести ban/restrict/delete в общий moderation action service с caller/reason и typed errors | При `CHAT_ADMIN_REQUIRED` каждое место возвращает/логирует typed outcome и не оставляет неоднозначный статус кейса |

## Рекомендуемый порядок исправлений

1. Закрыть `spam_vote` authorization: это самый вероятный exploit после `/ban`, потому что callback является публичной поверхностью.
2. Разделить admin panel permissions на read/general settings и security-critical actions.
3. Перевести reaction auto-ban в тот же voting/spam-case механизм или добавить учет eligible voters.
4. Спроектировать message-bound spam cases, чтобы `/ban` и LLM/voting всегда ссылались на конкретный target message.
5. После каждого исправления запускать как минимум `go test ./internal/handlers/chat ./internal/handlers/moderation`, затем `go test ./...` и `go vet ./...`.
