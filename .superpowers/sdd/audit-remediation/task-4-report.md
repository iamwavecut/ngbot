# Task 4: Moderation remediation report

## Commit

- Implementation commit: `8eb9d475a95ebfaa489fdd2d4323f1715b64579d`
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

## RED evidence

1. Focused moderation/router tests initially failed to compile because `PriorPermissionsJSON` did not exist; after exposing that seam, command spam still reached routing and untrusted `SenderChat` produced zero classifier calls.
2. `TestMandatoryModerationRouterPrecedesAdminConsumedCommands` initially failed because the mandatory handler constructor did not exist.
3. `TestJoinCaptchaWrongChoiceConsumesChallengeInOneAttempt` returned HTTP 200 instead of the terminal HTTP 403 response.
4. `TestVotingSurfacePersistenceFailureCompensatesBeforeModeration` observed `PreVoteRestricted=true` even though presentation failed before a mute.
5. `TestBanlistGuardChecksProviderBeforeJoinRequestFeatureRouting` showed a provider-banned join request proceeding to feature routing.
6. `TestMuteUserRestoresCapturedPermissionsWhenPersistenceFails` observed one restriction request instead of mute plus compensating restore.
7. `TestBanlistGuardModeratesMemberUpdateSubjectInsteadOfAdministratorActor` banned the administrator actor (`user_id=200`) instead of the member-update subject (`user_id=300`).

## GREEN evidence

- Focused Task 4 tests: PASS across moderation, chat, bot, and runtime ordering.
- `go test -race ./internal/handlers/chat ./internal/handlers/moderation ./internal/bot -run 'Test(BanlistGuard|UntrustedSenderChatSpam|CommandRunsProbation|JoinCaptchaWrongChoice|HandleJoinCaptchaAnswerAllows|UnmuteUserRestores|MuteUserRestores|RecordVoteRejects|VotingSurfacePersistence|UnrestrictChattingRestores)' -count=1`: PASS.
- `go test ./...`: PASS.
- `go vet ./...`: PASS.
- `go tool golangci-lint run --enable=unused --enable=unparam --enable=ineffassign --enable=goconst ./...`: PASS, zero issues.
- `git diff --check`: PASS.

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

## Verification note

A broad combined package race run produced no race-detector report but exceeded two Task 3 deadline-sensitive test assumptions under instrumentation (`TestAmbiguousWebAppResponseWaitsForBanCheckAndNeverFallsBack` and `TestJoinQueryResponseTimeoutIsDurablyActionable`). Each test passes independently under `-race`, and the focused Task 4 race suite plus the full normal suite pass. No production or external provider calls were made.
