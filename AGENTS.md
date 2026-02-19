# 🤖 Agent Development Rules & Guidelines

This document serves as the **single source of truth** for all development rules, coding standards, and agent behavior guidelines for this project.

## 📋 Table of Contents

- [🤖 Agent Development Rules \& Guidelines](#-agent-development-rules--guidelines)
  - [📋 Table of Contents](#-table-of-contents)
  - [📖 Codebase Overview](#-codebase-overview)
  - [🗣️ Communication \& Response Style](#️-communication--response-style)
  - [🏛️ Architecture \& Design](#️-architecture--design)
    - [Core Philosophy](#core-philosophy)
    - [Project Structure \& Module Organization](#project-structure--module-organization)
    - [Architecture Layers](#architecture-layers)
    - [External Services](#external-services)
  - [🐹 Go Development Rules](#-go-development-rules)
    - [Core Principles](#core-principles)
    - [Go Version \& Documentation](#go-version--documentation)
    - [Tool Dependencies](#tool-dependencies)
    - [Naming \& Structure](#naming--structure)
    - [Error Handling \& Types](#error-handling--types)
    - [Best Practices](#best-practices)
    - [Concurrency Rules](#concurrency-rules)
  - [🗄️ Database \& Schema Management](#️-database--schema-management)
    - [Migrations](#migrations)
    - [SQLC Configuration](#sqlc-configuration)
    - [Storage Layer](#storage-layer)
  - [📝 File Editing Strategy](#-file-editing-strategy)
    - [Core Principle](#core-principle)
    - [Mass Import Replacements](#mass-import-replacements)
    - [Best Practices (DOs \& DON'Ts)](#best-practices-dos--donts)
  - [🧹 Code Quality \& Hygiene](#-code-quality--hygiene)
    - [Linting \& Static Analysis](#linting--static-analysis)
    - [Cleanup Rules](#cleanup-rules)
  - [🔄 Workflow](#-workflow)
    - [Information Gathering](#information-gathering)
    - [Feedback \& Communication](#feedback--communication)
    - [Agent Workflow Steps](#agent-workflow-steps)

---

## 📖 Codebase Overview

**ngbot** is a Telegram gatekeeper bot with CAPTCHA verification, LLM-powered spam detection, and community voting moderation.

**Stack**: Go 1.25, SQLite, Telegram Bot API, OpenAI/Gemini LLMs

**Structure**:
- `cmd/ngbot/` - Entry point, runtime wiring
- `internal/bot/` - Core service, update processor
- `internal/handlers/` - Admin, Gatekeeper, Reactor, Moderation
- `internal/db/sqlite/` - Persistence with embedded migrations
- `internal/adapters/llm/` - OpenAI/Gemini clients
- `resources/` - i18n, challenges, migrations

For detailed architecture, see [docs/CODEBASE_MAP.md](docs/CODEBASE_MAP.md).

---

## 🗣️ Communication & Response Style

- **Language Policy** 🌐: Always reason and edit in English, but answer user in their prompt language.
- **Response Format** 📊: Always format responses using structured tables with emojis instead of long text blocks.
- **Visual Clarity** ✨: Use tables for better visual clarity and quick scanning. Replace lengthy paragraphs with concise, emoji-enhanced tabular format.
- **Present in diagrams** 📊: Present complex flows and business in Mermaid diagrams when appropriate.
- **Continuation Style** ⚡: Continue without stopping to reiterate or provide feedback, and don't report until all planned work is finished.
- **Web Search & Fetch** 🔍: Use ZAI MCP tools when needing to search or fetch web content.

---

## 🏛️ Architecture & Design

### Core Philosophy
- **Architecture Style** 🏗️: Hexagonal / Domain-Driven Design (DDD). Keep it modular and layered but compact. Avoid over-abstraction.
- **Avoid Over-Abstraction** 🎯: Don't abstract prematurely; keep solutions simple and focused.
- **Interfaces Near Consumer** 📍: Ports (interfaces) must be defined near the code that uses them.

### Project Structure & Module Organization
- `cmd/ngbot`: entrypoint and lifecycle wiring.
- `internal/bot`: core service, update processor, Telegram helpers.
- `internal/handlers`: admin, gatekeeper, reactor, moderation workflows.
- `internal/db/sqlite`: persistence with embedded migrations (including gatekeeper challenges).
- `internal/adapters/llm`: OpenAI/Gemini clients.
- `resources`: embedded i18n, gatekeeper challenges, migrations.
- Observability stack is removed; logs only.

### Architecture Layers
- Domain: `internal/db` entities and pure policies where applicable.
- Application: `internal/bot` and workflow handlers.
- Adapters: Telegram API, SQLite, LLM providers, banlist HTTP.

### External Services
- Telegram Bot API.
- LLM APIs (OpenAI-compatible and Gemini).
- Banlist API (lols.bot).

### Admin Panel UX Rules
- **Cascading Menus** 🧭: Admin settings must be structured as cascading category menus. Do not place many unrelated controls on a single page.
- **Leaf Screens** 🌿: Concrete value changes (presets/inputs) must live on leaf screens dedicated to one setting or one logical group.
- **i18n Completeness** 🌍: Any new admin UI key must be added for all supported locales before merge.

---

## 🐹 Go Development Rules

### Core Principles
- **Self-documenting code** 📖: No comments—clear names and structure speak for themselves.
- **No TODOs** 🚫: Write complete code or nothing. No placeholders.
- **Professional standards** 👨‍💻: Write like a professional Go developer would, without unnecessary code bloat.
- **Architecture first** 🏛️: Audit before coding: scan repo, read related packages, plan all changes.

### Go Version & Documentation
- **Go Version** 🔢: 1.25 (Latest features where applicable). Ref: [Go Release Notes](https://go.dev/doc/devel/release)
- **Documentation Strategy** 📚: Use `go doc`, `go tool`, `go list` for Go packages.
- **English Only** 🇺🇸: Code and technical reasoning in English.

### Tool Dependencies
- **Tool Directive** 🔧: Use Go 1.24+ `tool` directive in `go.mod` for dev tools (sqlc, golangci-lint, etc.).
- **No tools.go Hack** 🚫: Avoid the `tools.go` blank import pattern.
- **Refactor tooling** 🛠️:
  - `go tool gorename` — safe, reference-aware renames.
  - `go tool godoctor` — extract/inline functions and move code.
  - `go tool gopatch` — template-driven patches.
  - `go tool goimports` — auto-manage imports.

### Naming & Structure
- **Case Convention** 🔤: Use MixedCaps/mixedCaps (no underscores).
- **Acronyms** 🔤: All uppercase (HTTP, URL, ID, API).
- **Getters** 🎣: No "Get" prefix (`user.Name()` not `user.GetName()`).
- **Interfaces** 🔌: Ends in "-er" (Reader) or "-able" (Readable).
- **Organization** 📂: Group related constants/variables/types together.
- **Packages** 📁: One package per directory with short, meaningful names.
- **Formatting** ✨: Use `gofumpt -w .` (tabs, newline at EOF).

### Error Handling & Types
- **Check Immediately** ⚠️: Check errors immediately, no panic for normal errors.
- **Wrapping** 🎁: Use `fmt.Errorf("op: %w", err)`.
- **Inspection** 🔍: Use `errors.Is` / `errors.As`.
- **Interface Types** 🔄: Use `any` instead of `interface{}`.

### Best Practices
- **Testing** 🧪: Table-driven tests beside code (`*_test.go`). Mock interfaces.
- **Context** ⏱️: Use `context.Context` for cancellation/timeouts (first param).
- **Global Variables** 🚫: Avoid them.
- **Composition** 🔗: Prefer composition over inheritance.
- **Embedding** 📎: Use judiciously.
- **Preallocation** 🧠: Preallocate slices when length is known.

### Concurrency Rules
- **Philosophy** 🧠: Share memory by communicating.
- **Coordination** 📡: Channels for coordination, mutexes for state.
- **Error Groups** 👥: Use `errgroup` for concurrent tasks.
- **Leaks** 🚰: Prevent goroutine leaks.

---

## 🗄️ Database & Schema Management

### Migrations
- **Location** 📁: `internal/db/sql/migrations/` as numbered SQL files (e.g., `0_init.sql`).
- **Directives** 📝: Use `+migrate Up/Down`.
- **Functions** ⚙️: Wrap in `-- +migrate StatementBegin` and `-- +migrate StatementEnd`.

### SQLC Configuration
- **Queries** 📄: Defined in `internal/db/sql/queries.sql`.
- **Generated Code** 🔧: `internal/db/sqlc/` (`querier.go`, `models.go`, etc.).
- **Regeneration** ♻️: Run `go generate ./...`.
- **Embedding** 📎: Use `sqlc.embed` for cleaner structs.

### Storage Layer
- **Architecture** 🏗️: Interface → Base Postgres → Buffered/Cached → Factory.
- **Driver** 🔌: PostgreSQL with pgx v5.
- **Tools** 🛠️: `sqlc` for queries, `sql-migrate` for migrations.

---

## 📝 File Editing Strategy

### Core Principle
**Single-Action Complete Revisions**: Consolidate ALL necessary changes into bulk comprehensive updates. Deliver complete, functional code in a single edit action.

### Mass Import Replacements
- **Edge Case** 🔄: When restructuring requires updating imports across many files (30+), use `sed` or `find ... -exec sed`.
- **Example**: `find . -name "*.go" -exec sed -i '' 's|old/path|new/path|g' {} \;`
- **Verification**: Always run `go vet ./...` after mass replacements.

### Best Practices (DOs & DON'Ts)
- ✅ **Audit First**: Read and understand the complete file context.
- ✅ **Plan Comprehensively**: Identify all changes needed across the file.
- ✅ **Verify Completeness**: Ensure the edit delivers fully functional code.
- ❌ **No Placeholders**: No "TODO" or incomplete states.

---

## 🧹 Code Quality & Hygiene

### Linting & Static Analysis
- **Full Lint** 🔍: `go tool golangci-lint run --enable=unused --enable=unparam --enable=ineffassign --enable=goconst ./...`
- **Quick Check** ⚡: `go vet ./...` (Do not use `go build` for validation).
- **Compliance** ✅: **Never ignore lint warnings and fix them right away.**

### Cleanup Rules
- **Unused Code** 🗑️: Remove unused files, functions, parameters.
- **No Dead Code** 💀: Don't leave dead code unless protected by a describing comment.
- **Constants** 📦: Extract repeated string literals into constants.
- **Signature Refactoring** ✂️: Simplify function signatures by removing unused params/returns.

---

## 🔄 Workflow

### Information Gathering
- **Tool Usage** 🛠️: Use provided tools extensively instead of guessing.
- **Code Inspection** 🔍: Inspect code when unsure: list project structure, read whole files.
- **Dependency Check** 📦: Verify library existence in `go.mod` before importing.

### Feedback & Communication
- **Interactive Feedback** 💬: Always call `interactive_feedback` MCP when asking questions.
- **Continuous Feedback** 🔄: Continue calling until user feedback is empty.
- **Reporting** 📋: Request feedback or ask when finished or unsure.

### Agent Workflow Steps
1. **Audit First** 🔍: Read and understand the complete project structure (`tree -a -I .git`).
2. **Plan** 📋: Identify all changes needed across files.
3. **Verify** ✅: Use `go vet ./...` to check code; `go test ./...` if applicable.
4. **Update Docs** 📝: Update `AGENTS.md` if architecture changes or new rules introduced.
