# Task 4: Moderation remediation report

## Commit

- Implementation commit: `8eb9d475a95ebfaa489fdd2d4323f1715b64579d`
- Review-fix implementation commit: `d42729ffddb561fcfb2de481246921dc8eb40861`
- Review-fix round 2 implementation commit: `990fbc03d4641c1c4fcc4ee4eff5adf4621f045c`
- Base commit: `66bc10254740218d820f79c1ca5ca71451535661`

## Scope delivered

- Installed mandatory banlist and probation/content gates before configured feature handlers, including admin-consumed commands.
- Applied manual allowlist, moderation capability, cached/provider banlist, and feature-routing order to user-authored updates, joins, join requests, member-update subjects, reactions, callbacks, and votes.
- Added semantic checks for routed commands and mentions without permitting those messages to graduate probation.
- Classified untrusted `SenderChat` content and deleted/banned detected spam while preserving group-self/anonymous-admin and linked-channel trust.
- Required fresh Telegram membership plus banlist clearance for voters.
- Revalidated identity-aware manual allowlist and cached/provider banlist state on direct and WebApp CAPTCHA success.
- Reduced CAPTCHA to one attempt and removed the public word mapping from the active prompt; correct choices remain opaque server-side UUIDs and rotate per challenge.
- Restored effective chat defaults after CAPTCHA and persisted exact pre-mute permissions for voting/false-positive recovery; no path synthesizes all-true permissions.
- Compensated Telegram mutes when durable permission or vote-state persistence fails, and only marks `pre_vote_restricted` after a successful mute.
- Added migration `20260813190000-add-moderation-permission-snapshots.sql`.

## Review fix round 1

- Replaced enumerable word-choice CAPTCHA prompts with a per-challenge PNG arithmetic expression, independent localized instructions, randomized numeric choices, and opaque server-side success mapping. Direct CAPTCHA uses `sendPhoto`; WebApp embeds a validated PNG data URL. Wrong answers remain terminal and rotate on the next challenge.
- Split mandatory moderation from optional Reactor features. The mandatory router always runs banlist/capability and message/edit content policy; the optional feature wrapper contains only callbacks, reactions, commands, and mentions. Disabling Reactor therefore installs no feature processing and never leaves a nil detector in mandatory content moderation.
- Made `SenderChat` authoritative whenever present, including edits with a non-nil `From`, while retaining explicit linked-channel and anonymous-admin trust.
- Bound safe routed commands and mentions before returning, while preventing them from graduating probation, so post-graduation edits remain protected.
- Added target-chat, username-aware voter allowlist ordering and retained fresh membership/departure checks. The optional reaction feature path consumes the mandatory banlist decision instead of repeating the online provider lookup.
- Derived voting mute duration from the durable case deadline plus a recovery margin. Expired permission snapshots survive cleanup and are removed only after a successful restore.
- Reused the allowlist/capability/cached/provider revalidation order on CAPTCHA completion. Cached denies remain terminal without rights; online providers are called only after moderation rights are known available.

## Review fix round 2

- BanlistGuard now returns the exact chat/user/username allowlist and provider decision to the mandatory router.
- Spam-vote callbacks resolve the durable spam-case target chat before the mandatory guard, so target-chat username-aware allowlist priority is authoritative even when the callback message is hosted in a separate log channel.
- ReactorFeatures consumes that decision in the same mandatory-handler call. RecordVote reuses an exact target allowlist decision and a safe global provider decision, while still performing a target lookup when a log-chat decision does not cover the target identity.
- Full-chain coverage proves one provider call for a normal target voter, zero for a target allowlist match, zero for that match across a separate log chat, and one target decision when only the log chat is allowlisted.

## RED evidence

1. Focused moderation/router tests initially failed to compile because `PriorPermissionsJSON` did not exist; after exposing that seam, command spam still reached routing and untrusted `SenderChat` produced zero classifier calls.
2. `TestMandatoryModerationRouterPrecedesAdminConsumedCommands` initially failed because the mandatory handler constructor did not exist.
3. `TestJoinCaptchaWrongChoiceConsumesChallengeInOneAttempt` returned HTTP 200 instead of the terminal HTTP 403 response.
4. `TestVotingSurfacePersistenceFailureCompensatesBeforeModeration` observed `PreVoteRestricted=true` even though presentation failed before a mute.
5. `TestBanlistGuardChecksProviderBeforeJoinRequestFeatureRouting` showed a provider-banned join request proceeding to feature routing.
6. `TestMuteUserRestoresCapturedPermissionsWhenPersistenceFails` observed one restriction request instead of mute plus compensating restore.
7. `TestBanlistGuardModeratesMemberUpdateSubjectInsteadOfAdministratorActor` banned the administrator actor (`user_id=200`) instead of the member-update subject (`user_id=300`).
8. Round 1 RED tests exposed the public prompt/option relation, full Reactor installation when disabled, non-nil-`From` `SenderChat` bypass, missing routed-message binding, vote allowlist ordering, fixed mute duration, expired snapshot deletion, and provider calls in no-rights CAPTCHA completion.

## GREEN evidence

- Focused Task 4 tests: PASS across moderation, chat, bot, and runtime ordering.
- `go test -race ./internal/handlers/chat ./internal/handlers/moderation ./internal/bot -run 'Test(BanlistGuard|UntrustedSenderChatSpam|CommandRunsProbation|JoinCaptchaWrongChoice|HandleJoinCaptchaAnswerAllows|UnmuteUserRestores|MuteUserRestores|RecordVoteRejects|VotingSurfacePersistence|UnrestrictChattingRestores)' -count=1`: PASS.
- `go test ./...`: PASS.
- `go vet ./...`: PASS.
- `go tool golangci-lint run --enable=unused --enable=unparam --enable=ineffassign --enable=goconst ./...`: PASS, zero issues.
- `git diff --check`: PASS.
- Round 1 focused packages (`cmd/ngbot`, `internal/handlers/chat`, `internal/handlers/moderation`, `internal/db/sqlite`): PASS.
- Round 1 `go test ./...`: PASS.
- Round 1 `go vet ./...`: PASS.
- Round 1 strict golangci-lint command: PASS, zero issues.
- Round 1 focused package race run: `cmd/ngbot`, `moderation`, and `sqlite` PASS; the combined `chat` run hit the two pre-existing Task 3 deadline-sensitive tests named below, and both PASS together when rerun under `-race`.
- Round 2 full handler-chain callback regression: PASS normally and under `-race`.
- Round 2 `go test ./...`, `go vet ./...`, strict golangci-lint, and `git diff --check`: PASS.

## Decisions and invariants

- Handler order is banlist/capability gate, then probation/content router, then configured feature handlers.
- Capability is treated as three states: available permits provider checks and enforcement; confirmed unavailable prevents provider calls and Telegram moderation retries while allowing non-banned public CAPTCHA behavior; unknown stops feature routing.
- Join service updates are filtered by joined-member identity, never by the administrator actor; safe members remain available to Gatekeeper.
- Manual allowlist lookup is always first. Lookup failures grant no exemption.
- Commands and mentions can start or remain in probation and are classified, but cannot graduate it.
- A voter must be a current live member and not banlisted at vote time; cached membership cannot authorize a vote.
- CAPTCHA success is not authoritative until the current username-aware allowlist and banlist checks complete.
- Permission recovery uses either the captured member snapshot or current effective chat defaults, including restrictive defaults.
- Vote presentation is persisted before a mute; the durable mute marker is persisted only after the mute, with immediate unmute compensation on persistence failure.
- Public CAPTCHA metadata contains no prompt-to-answer equality relation; only the durable challenge associates the opaque option token with success.
- Mandatory content moderation and optional Reactor feature routing are separate handlers; optional features cannot re-run content or online banlist checks.
- Permission snapshots remain durable past Telegram mute expiry until an explicit successful restore deletes them.

## Verification note

A broad combined package race run produced no race-detector report but exceeded two Task 3 deadline-sensitive test assumptions under instrumentation (`TestAmbiguousWebAppResponseWaitsForBanCheckAndNeverFallsBack` and `TestJoinQueryResponseTimeoutIsDurablyActionable`). Each test passes independently under `-race`, and the focused Task 4 race suite plus the full normal suite pass. No production or external provider calls were made.
