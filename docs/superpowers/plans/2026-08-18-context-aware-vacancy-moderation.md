# Context-aware vacancy moderation implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make detailed vacancies safe by default and explicitly on-topic in Jobs & HR chats without weakening scam and gambling-ad detection.

**Architecture:** Persist a small per-chat moderation-profile enum and a binary label on chat examples. Pass both through a typed classification context to the existing LLM detector, whose static policy defines the corrected semantic boundary. Reuse the existing cascading admin panel for profile selection and labeled example management.

**Tech Stack:** Go 1.25.13, SQLite migrations, sqlx, Telegram Bot API, Gemini/OpenAI-compatible LLM adapters, YAML i18n.

**Spec:** `docs/superpowers/specs/2026-08-18-context-aware-vacancy-moderation-design.md`

## Global Constraints

- Preserve existing chats as `general` and existing examples as spam.
- A contact CTA alone never establishes spam.
- iGaming employment is not gambling promotion.
- Candidate/profile/admin data stays live, structured, and untrusted.
- Every new admin UI key must exist in every supported locale.
- Do not modify `docs/CODEBASE_MAP.md`.
- Deploy only the merged, verified revision through `scripts/release.sh`.

---

### Task 1: Persist moderation profile and labeled examples

**Files:**
- Modify: `internal/db/entities.go`
- Modify: `internal/db/settings.go`
- Modify: `internal/db/settings_test.go`
- Modify: `internal/db/sqlite/client_settings_members.go`
- Modify: `internal/db/sqlite/admin_panel.go`
- Modify: `internal/db/sqlite/migrations_test.go`
- Create: `resources/migrations/20260818000000-add-context-aware-moderation.sql`

**Interfaces:**
- Produces: `db.LLMModerationProfileGeneral`, `db.LLMModerationProfileJobsHR`, `db.SpamClassificationAllowed`, `db.SpamClassificationSpam`.
- Produces: `Settings.LLMModerationProfile` and `ChatSpamExample.Classification`.
- Produces filtered list/count methods accepting `classification int`.

- [x] Write failing tests for defaults, profile normalization, example labels, filtered queries, and migration columns.
- [x] Run focused DB tests and confirm failures are caused by missing fields/schema.
- [x] Add the migration and minimal persistence implementation.
- [x] Run focused DB tests and confirm they pass.

### Task 2: Correct the classifier boundary and carry chat context

**Files:**
- Modify: `internal/handlers/moderation/spam_detector.go`
- Modify: `internal/handlers/moderation/spam_detector_test.go`
- Modify: `internal/handlers/chat/reactor.go`
- Modify: `internal/handlers/chat/reactor_message_pipeline.go`
- Modify: `internal/handlers/chat/reactor_message_pipeline_test.go`
- Modify: `internal/handlers/chat/reactor_reaction_profile_check.go`

**Interfaces:**
- Produces: `moderation.ClassificationContext{Profile string, Examples []ClassificationExample}`.
- Consumes: persisted profile and labeled chat examples from Task 1.

- [x] Write failing detector tests for structured profile/example framing and paired vacancy/scam boundaries.
- [x] Write failing reactor tests for loading both labels and propagating the Jobs & HR profile.
- [x] Run focused moderation/chat tests and confirm expected failures.
- [x] Implement the typed context and corrected prompts with representative safe/spam boundary examples.
- [x] Run focused moderation/chat tests and confirm they pass.

### Task 3: Expose profile and safe examples in the admin panel

**Files:**
- Modify: `internal/handlers/admin/admin.go`
- Modify: `internal/handlers/admin/panel_types.go`
- Modify: `internal/handlers/admin/panel_session_service.go`
- Modify: `internal/handlers/admin/panel_renderer.go`
- Modify: `internal/handlers/admin/panel_commands.go`
- Modify: `internal/handlers/admin/panel_render.go`
- Modify: `internal/handlers/admin/panel_handler.go`
- Modify: `internal/handlers/admin/panel_recommended.go`
- Modify or create focused tests in `internal/handlers/admin/`
- Modify: `resources/i18n/translations.yml`

**Interfaces:**
- Consumes: profile and classification constants plus filtered persistence from Task 1.
- Produces: LLM profile leaf screen and separate safe/spam example lists using the existing workflow.

- [x] Write failing tests for state synchronization, profile selection, and classification-preserving example creation.
- [x] Run focused admin and i18n tests and confirm expected failures.
- [x] Implement the leaf screen, commands, labeled list workflow, and complete locale keys.
- [x] Run focused admin and i18n tests and confirm they pass.

### Task 4: Document and validate the complete change

**Files:**
- Modify: `README.md`

**Interfaces:**
- Documents the operator-visible profile and safe-example behavior.

- [x] Update README behavior and admin-panel instructions.
- [x] Run `gofmt` on changed Go files and `git diff --check`.
- [x] Run focused tests, `go vet ./...`, `go test ./...`, `go test -race ./...`, `go test -shuffle=on ./...`, configured golangci-lint, `go mod tidy -diff`, and `docker build .`.
- [x] Inspect the complete diff against the design and verify no unrelated changes.

### Task 5: Publish, release, and verify production

**Files:**
- Use: `.github/workflows/ci.yml`
- Use: `scripts/release.sh`
- Use: `scripts/validate-deployment.sh`

**Interfaces:**
- Produces a merged GitHub revision and the matching production image.

- [ ] Commit the verified scope and push `agent/context-aware-vacancy-moderation`.
- [ ] Open a PR, wait for terminal CI, and merge only the verified head.
- [ ] Run the production release from the current `master` revision.
- [ ] Verify live revision/image, container health and restart count, migration presence, SQLite integrity, probes, bounded live classification cases, and fresh logs.
