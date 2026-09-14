# Comment author trust implementation

User-approved specification: all chats use three safe distinct new messages to grant 30 days of per-chat trust to users and sender chats. Expiry requires one safe new message for renewal. Commands, bot mentions, edits, reactions, and empty content never advance trust. Preserve banlist priority, manual user overrides, linked-channel and anonymous-administrator exemptions, durable retries and protected edit checks.

## Constraints

- Preserve the repository-pinned Go toolchain and current SQLite/Telegram/LLM adapters. Implementation was validated on Go 1.25.13; release integration retains the Go 1.26.8 security update already merged in `77f0bda` and repeats verification on that toolchain. This feature adds no dependencies.
- The user authorized commits and production deployment after local implementation. Publish through the existing PR/CI and `scripts/release.sh` flow; preserve backups and verify the exact running revision. Do not edit docs/CODEBASE_MAP.md.
- User and sender_chat are distinct author kinds. SenderChat wins over technical From everywhere.
- History: same chat/thread only, five preceding messages from 24 hours, direct reply and root post first, 2000 runes per persisted text, 8000 runes total extra context. Keep context separate from trust and out of logs.
- Pending spam cases suspend trust. Confirmed spam resets it; false positives restore any remaining earlier expiry. Channel cases respect the chat voting setting; delete suspected messages without trying to mute a channel before a vote. Ban channels only after confirmation, immediately if voting is disabled.
- Migrate previously effective user trust for 30 days; active probation starts at zero; reaction-only memory never grants trust. Preserve bindings, cases, votes and durable work.

## Task 1: Persistence

Owned by the persistence implementer: new domain author/trust/context types; new SQLite trust/context files and migrations; existing challenged-message adapter and retention adapter; focused database tests. Do not edit reactor, spam control, config, docs or existing entities.go (root owns the latter).

Interfaces, all on sqliteClient:

- db.MessageAuthor {Kind string; ID int64}; constants MessageAuthorUser="user", MessageAuthorSenderChat="sender_chat". Validate positive user IDs and negative sender-chat IDs; do not reinterpret legacy unknown kinds.
- db.MessageTrust {ChatID int64; AuthorKind string; AuthorID int64; SafeMessages int; TrustedUntil sql.NullTime; Suspended bool}. Trusted(now) returns valid future expiry and !Suspended.
- MessageTrust(ctx, chatID, author) (*db.MessageTrust,error); EnsureMessageTrust(ctx, chatID, author) (*db.MessageTrust,error).
- RecordSafeAuthorMessage(ctx, chatID, author, messageID int, now time.Time, requiredMessages int, trustDuration time.Duration, eligible bool) (*db.MessageTrust,bool,error). Bind message and advance trust atomically only for a newly inserted binding, eligible=true, and no pending/resolving case. Initial threshold requiredMessages; an expired mature trust renews on one eligible distinct message. Returned bool says a binding was newly inserted. Errors must rollback both writes. Counter caps at threshold. Pending/resolving cases preserve old counter/expiry without advancing them.
- IsCheckedAuthorMessage(ctx, chatID, author, messageID int) (bool,error).
- ResetMessageTrust(ctx,chatID,author) error: zero counter and expiry, retain bindings for edits.
- db.MessageContext {ChatID int64; MessageID int; ThreadID int; ReplyToMessageID int; AuthorKind string; AuthorID int64; Text string; SentAt time.Time; UpdatedAt time.Time; UpdateID int}.
- UpsertMessageContext(ctx,*db.MessageContext) error: truncate text to 2000 runes, ignore stale edit versions, preserve original SentAt; replace with empty text for cleared content.
- MessageContext(ctx,chatID,messageID int) (*db.MessageContext,error).
- RecentMessageContext(ctx,chatID int64,threadID,beforeMessageID int,after time.Time,limit int) ([]db.MessageContext,error): strict same chat+thread, smaller message IDs, SentAt >= after, nonempty text, newest first.
- DeleteMessageContext(ctx,chatID,messageID int) error. A bot-deleted row must not be resurrected by late/replayed updates during the context retention window; use a tombstone if needed.

Migration details: create chat_author_trust and chat_message_context. Add author_kind TEXT NOT NULL DEFAULT 'user' to chat_challenged_messages and spam_cases. Keep legacy user_id columns and methods for compatibility, but typed APIs must match kind. Suspended computed from matching spam_cases in pending/resolving_spam/resolving_false_positive. Backfill graduated probations and chat_members except active probations; do not grant from chat_known_non_members alone. Preserve prior grants for identities with active spam cases under computed suspension, so a false-positive resolution can restore the still-valid expiry. All existing spam_cases/bindings remain kind=user. Ensure old legacy probation APIs/tests remain available until root removes consumers. Add Up/Down migration; no rewriting applied migrations. Retention uses existing bounded cleanup, deletes expired context using SentAt, retaining exact checked-message bindings across expiry, renewal and resets (no age cleanup; reclaim by chat cascade). Add tests for restart, concurrency, rollback, expiry, suspension/reset, migration backfill/exclusions, thread isolation, edits/tombstones/retention.

## Task 2: Pipeline and context

Root owns reactor*.go, moderation_router.go, new message author/context helpers, config and UI/docs. Replace timed probation with the typed transactional trust API. Keep membership bookkeeping best effort after trust commits. Authoritative sender identity on new messages, edits, reports and exhausted failures. Remove external-reply automatic spam heuristic. Persist context for trusted/skipped authors when moderation is enabled; ignore stale versions. Context passed as untrusted structured fields to classification, classify candidate only. Add behavioral tests before implementation and update superseded time-based tests.

## Task 3: Moderation cases

Separate implementer after persistence: typed spam case author, typed queries, per-author case resolution, channel voting, resetting trust on confirmed spam, context deletion on bot deletions, presentation of channels without fake user mentions; preserve users/retry behavior and voter membership checks. Root integrates report command route. Add channel case and recovery regression tests.

## Task 4: Finish

Update global config (SafeMessagesRequired=3, AuthorTrustDuration=720h; deprecated MessageProbationDuration ignored with warning), Compose, all locale UI text and documentation. Run current CI checks including formatting, tidy diff, whitespace, vet, tests, race, shuffle, lint, govulncheck, deployment validation and container migration checks when available. Review complete diff independently; fix material findings and rerun affected checks. Report only verified local outcome and any unavailable external validation.
