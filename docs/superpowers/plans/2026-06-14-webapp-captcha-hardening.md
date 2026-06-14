# WebApp Join-Request CAPTCHA — Hardening & Fast Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the verified correctness/security defects in the join-request WebApp CAPTCHA flow and make the DM fallback fire ~11s after it is clear the Mini App was never opened (instead of after the full 3-minute timeout), without ever orphaning a join request or banning a legitimate user.

**Architecture:** Three phases. **Phase A** = self-contained correctness/security fixes that touch single functions and ship immediately. **Phase B** = the fast-fallback feature: a new `web_app_opened_at` column, an idempotent open-marker recorded on the GET page, a dedicated short-interval sweep, and an atomic compare-and-set claim that unifies all fallback paths and eliminates the approve-vs-fallback race. Tests are written first (TDD) for every behavioral change.

**Tech Stack:** Go 1.25, `modernc.org/sqlite` via `sqlx` (struct scan by `db:` tags), `sql-migrate` migrations under `resources/migrations/`, `OvyFlash/telegram-bot-api`, table-driven tests with the in-memory `gatekeeperFlowStore` mock + `newTestBotAPI` fake transport.

---

## Background: verified findings this plan addresses

| ID | Defect | Phase | Severity |
|----|--------|-------|----------|
| C1 | WebApp send-fail deletes row + returns err → join request orphaned | A | HIGH |
| C5 | `approve` called after `status=Passed` persisted; 502 loses a correct answer | A | HIGH |
| C7 | `declineWebAppChallenge` overwrites decline error with delete error | A | LOW |
| N1 | `auth_date` never validated → initData replayable | A | MED |
| N2 | WebApp approve path never re-checks ban | A | MED |
| N10 | HTTP server has no Read/Write/Idle timeouts | A | LOW |
| C2 | `GetChat(group)` failure → bare return → infinite re-fallback loop + orphan | B | HIGH |
| C6 | `startChallenge` upsert-fail in fallback → loop (no delete) | B | LOW |
| C8 | dead `CommChatID==0` branch + stale `newWebAppChallenge` test helper | B | LOW |
| C3/N5 | scheduler-vs-HTTP race overwrites a concurrently-approved row → legit ban / double-answer | B | MED |
| C4/N12 | non-atomic marker re-attach can orphan on a concurrent newer row | B | LOW |
| C10 | no "opened" signal → fast fallback impossible | B | MED (feature) |

**Explicitly out of scope (judgment calls):**
- **Other-agent audit point 9** (`AND status='pending'` on `GetExpiredChallenges`): NOT done — the scheduler is the reaper for `passed_waiting_member_join` handoff rows too; filtering would break their cleanup.
- **N7 default listen addr → loopback**: NOT done as code — the documented docker deployment maps `127.0.0.1:18080 → container:8080`, which requires the in-container server to bind `0.0.0.0:8080`. Binding loopback would break prod. The listen addr is already config-driven (`GatekeeperWebApp.ListenAddr`). Captured as a deployment doc note (Task A6).
- **N7 XOR "obfuscation"**: left as-is. It only deters casual HTML scraping; the real gate is the initData HMAC (a valid Telegram session per attempt). Acceptable for the gatekeeper threat model; redesigning to fully server-side answer checking is a separate effort.
- **N3 (re-request upsert), N4 (non-unique token index), N6, N8, N9**: low-value/low-probability, deferred.

---

## File Structure

| File | Responsibility | Phase |
|------|----------------|-------|
| `internal/handlers/chat/gatekeeper_webapp.go` | send-fail fallback (C1), approve ordering (C5), decline error join (C7), auth_date check (N1), ban recheck (N2), server timeouts (N10), open-marker hook (B) | A + B |
| `internal/handlers/chat/gatekeeper_scheduler.go` | unified atomic fallback, claim, expired+unopened sweeps (C2/C3/C4/C6/C8/C10/N5/N12) | B |
| `internal/handlers/chat/gatekeeper.go` | new `gatekeeperStore` methods, new worker + interval | B |
| `internal/handlers/chat/gatekeeper_challenge_service.go` | `webAppOpenDeadline` const | B |
| `internal/db/entities.go` | `Challenge.WebAppOpenedAt`, `ChallengeStatusWebAppFallbackPending` | B |
| `internal/db/dependencies.go` | `db.Client` interface: 3 new methods | B |
| `internal/db/sqlite/client_challenges.go` | column in all queries; 3 new methods | B |
| `resources/migrations/20260614120000-add-gatekeeper-challenge-opened-at.sql` | new nullable column + partial index | B |
| `internal/handlers/chat/gatekeeper_join_flow_test.go` | mock store new methods + flow/race tests | B |
| `internal/handlers/chat/gatekeeper_webapp_test.go` | A-phase tests + fix stale `newWebAppChallenge` | A + B |

---

# PHASE A — Correctness & security fixes (shippable independently)

These touch single functions, need no schema change, and can be committed/deployed before Phase B. They address the highest-frequency failures (any transient Telegram error).

## Task A1: C1 — never orphan a join request on WebApp send failure

**Files:**
- Modify: `internal/handlers/chat/gatekeeper_webapp.go:687-692` (inside `startJoinRequestWebAppChallenge`)
- Test: `internal/handlers/chat/gatekeeper_webapp_test.go`

- [ ] **Step 1: Write the failing test**

Add to `gatekeeper_webapp_test.go`. The fake transport fails `sendChatJoinRequestWebApp` and records whether the request is then queued.

```go
func TestStartJoinRequestWebAppChallengeQueuesOnSendFailure(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		switch method {
		case testTelegramMethodSendJoinWebApp:
			http.Error(httptest.NewRecorder(), "boom", http.StatusBadGateway)
			return apiError(http.StatusBadGateway, "Bad Gateway")
		case testTelegramMethodJoinRequestQuery:
			return true
		default:
			t.Fatalf("unexpected method: %s", method)
			return nil
		}
	})

	store := newGatekeeperFlowStore()
	g := &Gatekeeper{
		s:          &gatekeeperTestService{testBotService: testBotService{botAPI: botAPI, language: "en"}, settings: webAppSettings()},
		store:      store,
		config:     &config.Config{GatekeeperWebApp: config.GatekeeperWebApp{PublicURL: "https://example.test"}},
		banChecker: &testGatekeeperBanChecker{},
	}

	req := &api.ChatJoinRequest{
		Chat:       api.Chat{ID: -100123, Type: "supergroup"},
		From:       api.User{ID: 42, FirstName: "Neo"},
		UserChatID: 9001,
		QueryID:    "join-query",
	}

	err := g.startJoinRequestWebAppChallenge(context.Background(), req, webAppSettings())
	if err == nil {
		t.Fatal("expected send error to propagate")
	}
	if len(store.challenges) != 0 {
		t.Fatalf("expected challenge row deleted after send failure, got %d", len(store.challenges))
	}
	if got := len(recorder.byMethod(testTelegramMethodJoinRequestQuery)); got != 1 {
		t.Fatalf("expected one queue answer after send failure, got %d", got)
	}
}
```

If `apiError`/`httptest` helpers are not already present in the test file, reuse the existing failure idiom the file already uses for forcing Telegram errors (grep `newTestBotAPI` callers that return errors); the assertion that matters is: row deleted + exactly one `answerChatJoinRequestQuery` call.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/chat/ -run TestStartJoinRequestWebAppChallengeQueuesOnSendFailure -v`
Expected: FAIL — no `answerChatJoinRequestQuery` call recorded (current code only deletes + returns).

- [ ] **Step 3: Implement the fix**

Replace the send-failure block at `gatekeeper_webapp.go:687-692`:

```go
	if err := bot.SendJoinRequestWebApp(ctx, g.s.GetBot(), request.QueryID, webAppURL); err != nil {
		if deleteErr := g.store.DeleteChallenge(ctx, challenge.CommChatID, challenge.UserID, challenge.ChatID); deleteErr != nil {
			entry.WithField("error", deleteErr.Error()).Error("failed to delete unsent web app challenge")
		}
		if queueErr := bot.AnswerJoinRequestQuery(ctx, g.s.GetBot(), request.QueryID, bot.JoinRequestQueryResultQueue); queueErr != nil {
			entry.WithField("error", queueErr.Error()).Error("failed to queue join request after web app send failure")
		}
		return errors.Wrap(err, "send web app challenge")
	}
```

Rationale: deleting the row keeps the table clean; queuing leaves the request in the admins' normal pending list instead of silently lost. The queue call may itself fail if the 10s query window already lapsed — that is logged and harmless (the request stays pending regardless).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/chat/ -run TestStartJoinRequestWebAppChallengeQueuesOnSendFailure -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/chat/gatekeeper_webapp.go internal/handlers/chat/gatekeeper_webapp_test.go
git commit -m "fix(gatekeeper): queue join request when web app send fails instead of orphaning"
```

---

## Task A2: C5 — approve before persisting Passed; keep pending on approve failure

**Files:**
- Modify: `internal/handlers/chat/gatekeeper_webapp.go:914-923` (inside `handleJoinCaptchaAnswer`)
- Test: `internal/handlers/chat/gatekeeper_webapp_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestHandleJoinCaptchaAnswerKeepsPendingWhenApproveFails(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodJoinRequestQuery:
			return apiError(http.StatusBadGateway, "Bad Gateway")
		default:
			t.Fatalf("unexpected method: %s", method)
			return nil
		}
	})

	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(time.Minute))
	if _, err := store.CreateChallenge(context.Background(), challenge); err != nil {
		t.Fatalf("seed challenge: %v", err)
	}
	g := newWebAppTestGatekeeper(t, botAPI, store) // existing helper used by other answer tests

	form := url.Values{
		"token":     {challenge.WebAppToken},
		"choice":    {challenge.SuccessUUID},
		"init_data": {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)},
	}
	rr := postJoinCaptchaAnswer(t, g, form) // existing helper that calls handleJoinCaptchaAnswer

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr.Code)
	}
	got := store.onlyChallenge(t)
	if got.Status != db.ChallengeStatusPending {
		t.Fatalf("expected status to remain pending after approve failure, got %q", got.Status)
	}
}
```

Use whatever helper names already exist for invoking `handleJoinCaptchaAnswer` and constructing the gatekeeper in the existing webapp answer tests (grep `handleJoinCaptchaAnswer(` in `gatekeeper_webapp_test.go` and mirror the setup). The key assertions: 502 returned AND status still `pending`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/chat/ -run TestHandleJoinCaptchaAnswerKeepsPendingWhenApproveFails -v`
Expected: FAIL — current code persists `passed_waiting_member_join` before approve, so status is not pending.

- [ ] **Step 3: Implement the fix**

Replace `gatekeeper_webapp.go:914-923` (the success branch) so approve happens first and the terminal status is committed only after approve succeeds:

```go
	if err := bot.AnswerJoinRequestQuery(r.Context(), g.s.GetBot(), challenge.JoinRequestQueryID, bot.JoinRequestQueryResultApprove); err != nil {
		g.getLogEntry().WithField("error", err.Error()).Error("failed to approve join request query")
		writeJoinCaptchaJSON(w, http.StatusBadGateway, joinCaptchaAnswerResponse{Message: copy.CouldNotApprove})
		return
	}
	challenge.Status = db.ChallengeStatusPassedWaitingMemberJoin
	challenge.ExpiresAt = time.Now().Add(approvedJoinRequestChallengeTTL)
	if err := g.store.UpdateChallenge(r.Context(), challenge); err != nil {
		g.getLogEntry().WithField("error", err.Error()).Error("failed to persist passed join request challenge after approve")
		writeJoinCaptchaJSON(w, http.StatusInternalServerError, joinCaptchaAnswerResponse{Message: copy.CouldNotSaveResult})
		return
	}
```

Approve is idempotent for a still-pending Telegram request, so a client retry after a transient 502 is safe (the row stays `pending`, so the retry POST is accepted rather than 404'd). Residual (accepted): if approve succeeds but the subsequent `UpdateChallenge` fails, the user is approved on Telegram while our row stays `pending`; on later expiry the scheduler may attempt a decline that Telegram no-ops (request already processed). This is a logged no-op, not a wrong outcome.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/chat/ -run TestHandleJoinCaptchaAnswerKeepsPendingWhenApproveFails -v`
Expected: PASS. Also run the existing happy-path answer test to confirm no regression:
Run: `go test ./internal/handlers/chat/ -run TestHandleJoinCaptchaAnswer -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/chat/gatekeeper_webapp.go internal/handlers/chat/gatekeeper_webapp_test.go
git commit -m "fix(gatekeeper): approve join request before persisting passed status"
```

---

## Task A3: C7 — aggregate decline+delete errors instead of overwriting

**Files:**
- Modify: `internal/handlers/chat/gatekeeper_webapp.go` imports + `declineWebAppChallenge` (lines ~1007-1019)

- [ ] **Step 1: Add the stdlib errors alias**

The file imports `github.com/pkg/errors` as `errors` (line 23). Add a stdlib alias to the import block:

```go
	stderrors "errors"
```

- [ ] **Step 2: Aggregate the errors**

Change the delete branch in `declineWebAppChallenge` (line ~1015-1017) from overwrite to join:

```go
	if err := g.store.DeleteChallenge(ctx, challenge.CommChatID, challenge.UserID, challenge.ChatID); err != nil {
		result = stderrors.Join(result, errors.WithMessage(err, "delete declined web app challenge"))
	}
	return result
```

`stderrors.Join(nil, x)` returns `x` and drops nils, so when only one of the two operations fails behavior is unchanged; when both fail, both are now reported.

- [ ] **Step 3: Verify build**

Run: `go vet ./internal/handlers/chat/...`
Expected: no errors (confirms the alias is used and compiles).

- [ ] **Step 4: Commit**

```bash
git add internal/handlers/chat/gatekeeper_webapp.go
git commit -m "fix(gatekeeper): aggregate decline and delete errors in declineWebAppChallenge"
```

---

## Task A4: N1 — reject stale WebApp initData (auth_date freshness)

**Files:**
- Modify: `internal/handlers/chat/gatekeeper_webapp.go` — `webAppInitData` struct (76-79), `parseWebAppInitData` (1204-1219), `handleJoinCaptchaAnswer` (after the initData validation block ~875)
- Add const near other webapp consts (26-35)
- Test: `internal/handlers/chat/gatekeeper_webapp_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestHandleJoinCaptchaAnswerRejectsStaleInitData(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		t.Fatalf("no telegram call expected for stale init data, got %s", method)
		return nil
	})
	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(time.Minute))
	_, _ = store.CreateChallenge(context.Background(), challenge)
	g := newWebAppTestGatekeeper(t, botAPI, store)

	stale := staleSignedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID, time.Now().Add(-2*time.Hour))
	form := url.Values{"token": {challenge.WebAppToken}, "choice": {challenge.SuccessUUID}, "init_data": {stale}}
	rr := postJoinCaptchaAnswer(t, g, form)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for stale init data, got %d", rr.Code)
	}
}
```

Add a `staleSignedWebAppInitData` helper that mirrors `signedWebAppInitData` but takes an explicit `authDate time.Time` (copy the existing helper, replacing `time.Now().Unix()` with `authDate.Unix()`). Then refactor `signedWebAppInitData` to delegate: `return staleSignedWebAppInitData(t, token, queryID, userID, time.Now())`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/chat/ -run TestHandleJoinCaptchaAnswerRejectsStaleInitData -v`
Expected: FAIL — current code ignores `auth_date`, so the answer proceeds.

- [ ] **Step 3: Implement**

Add the TTL const in the `const (...)` block (lines 26-35):

```go
	joinCaptchaInitDataTTL = time.Hour
```

Extend the struct (76-79):

```go
type webAppInitData struct {
	QueryID  string
	UserID   int64
	AuthDate int64
}
```

Extend `parseWebAppInitData` (1204-1219) to read `auth_date`:

```go
	authDate, _ := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	return webAppInitData{
		QueryID:  values.Get("query_id"),
		UserID:   user.ID,
		AuthDate: authDate,
	}, nil
```

(Add `"strconv"` to imports if not present — it is not currently imported in this file; add it.)

In `handleJoinCaptchaAnswer`, immediately after `initData, err := parseWebAppInitData(initDataRaw)` succeeds and before the user/query match check (~875), add:

```go
	if initData.AuthDate == 0 || time.Since(time.Unix(initData.AuthDate, 0)) > joinCaptchaInitDataTTL {
		writeJoinCaptchaJSON(w, http.StatusUnauthorized, joinCaptchaAnswerResponse{Message: copy.TelegramCheckFailed})
		return
	}
```

A 1-hour TTL bounds replay while tolerating clock skew; the challenge itself lives only minutes, so legitimate flows are never affected.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/handlers/chat/ -run 'TestHandleJoinCaptchaAnswer' -v`
Expected: PASS (stale rejected; fresh happy-path still passes).

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/chat/gatekeeper_webapp.go internal/handlers/chat/gatekeeper_webapp_test.go
git commit -m "fix(gatekeeper): reject stale web app init data to prevent replay"
```

---

## Task A5: N2 — re-check ban before approving via WebApp

**Files:**
- Modify: `internal/handlers/chat/gatekeeper_webapp.go` — `handleJoinCaptchaAnswer`, just before the approve call (after Task A2 reordering)
- Test: `internal/handlers/chat/gatekeeper_webapp_test.go`

- [ ] **Step 1: Write the failing test**

`testGatekeeperBanChecker` must expose a way to mark a user known-banned. If it does not already, add a `knownBanned map[int64]bool` field and have `IsKnownBanned` consult it.

```go
func TestHandleJoinCaptchaAnswerDeclinesKnownBannedUser(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		switch method {
		case testTelegramMethodJoinRequestQuery:
			return true // decline expected, not approve
		default:
			t.Fatalf("unexpected method: %s", method)
			return nil
		}
	})
	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(time.Minute))
	_, _ = store.CreateChallenge(context.Background(), challenge)
	banChecker := &testGatekeeperBanChecker{knownBanned: map[int64]bool{challenge.UserID: true}}
	g := newWebAppTestGatekeeperWithBan(t, botAPI, store, banChecker)

	form := url.Values{"token": {challenge.WebAppToken}, "choice": {challenge.SuccessUUID}, "init_data": {signedWebAppInitData(t, botAPI.Token, challenge.JoinRequestQueryID, challenge.UserID)}}
	rr := postJoinCaptchaAnswer(t, g, form)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for banned user, got %d", rr.Code)
	}
	if len(store.challenges) != 0 {
		t.Fatalf("expected challenge deleted after decline, got %d", len(store.challenges))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/chat/ -run TestHandleJoinCaptchaAnswerDeclinesKnownBannedUser -v`
Expected: FAIL — current code approves without a ban check.

- [ ] **Step 3: Implement**

In `handleJoinCaptchaAnswer`, in the correct-answer branch, before the approve call added in A2 (and after the `isTestWebAppChallenge` early-return at ~905-912), add:

```go
	if g.banChecker != nil && g.banChecker.IsKnownBanned(challenge.UserID) {
		if err := g.declineWebAppChallenge(r.Context(), challenge); err != nil {
			g.getLogEntry().WithField("error", err.Error()).Error("failed to decline banned web app challenge")
		}
		writeJoinCaptchaJSON(w, http.StatusForbidden, joinCaptchaAnswerResponse{OK: false, Done: true, Message: copy.Blocked})
		return
	}
```

`IsKnownBanned` is the cached, non-network method (already on `GatekeeperBanChecker`, `gatekeeper.go:85`), so it is safe to call synchronously inside the HTTP handler.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/handlers/chat/ -run 'TestHandleJoinCaptchaAnswer' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/chat/gatekeeper_webapp.go internal/handlers/chat/gatekeeper_webapp_test.go
git commit -m "fix(gatekeeper): re-check known-banned status before web app approve"
```

---

## Task A6: N10 — add HTTP server timeouts (+ N7 deployment doc note)

**Files:**
- Modify: `internal/handlers/chat/gatekeeper_webapp.go:579-583` (`startWebAppServer`)

- [ ] **Step 1: Add timeouts**

Replace the `http.Server` construction in `startWebAppServer` (579-583):

```go
	server := &http.Server{
		Addr:              listenAddr,
		Handler:           g.joinCaptchaWebAppHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
```

These bound slow-client (slowloris-style) connections. The Mini App's only requests are a small GET and a small POST, so 15s is generous.

- [ ] **Step 2: Document the bind/proxy requirement**

Append to `AGENTS.md` under "External Services" (or the gatekeeper webapp section) a short note: the WebApp server speaks plain HTTP and MUST sit behind a TLS-terminating reverse proxy; the listen address is set via `GatekeeperWebApp.ListenAddr` and in the docker deployment binds `0.0.0.0:8080` inside the container (mapped to `127.0.0.1:18080` on the host). Do not change the default to loopback — it would break the container port mapping.

- [ ] **Step 3: Verify build + commit**

Run: `go vet ./internal/handlers/chat/...`
Expected: no errors

```bash
git add internal/handlers/chat/gatekeeper_webapp.go AGENTS.md
git commit -m "fix(gatekeeper): add web app server read/write/idle timeouts; document proxy requirement"
```

---

## Phase A checkpoint

- [ ] Run full suite + vet:

Run: `go test ./... && go vet ./...`
Expected: PASS. Phase A is independently deployable.

---

# PHASE B — Fast fallback feature + atomic fallback (C2, C3, C4, C6, C8, C10, N5, N12)

Adds the "opened" signal, a dedicated short sweep, and an atomic compare-and-set claim that all fallback paths flow through. This rewrites `fallbackExpiredWebAppChallenge` and the scheduler dispatch, eliminating the loop (C2/C6), the race (C3/N5), the marker-loss (C4/N12), and the dead branch (C8).

**Design summary:**
- New nullable column `web_app_opened_at`. The open deadline is **computed** as `created_at + webAppOpenDeadline` (no second column needed).
- `handleJoinCaptcha` (GET) records the open via an idempotent guarded UPDATE.
- A new worker every 3s selects un-opened, past-open-deadline WebApp challenges and falls them back early.
- Both the un-opened sweep and the expired sweep go through `claimWebAppChallengeForFallback` — a single guarded UPDATE flipping `status` `pending → web_app_fallback_pending` only if still pending, still a WebApp join-request row, and still un-opened. `rowsAffected==1` ⇔ we own it. A concurrent correct answer (which flips to `passed_waiting_member_join`) or a concurrent open (which sets `web_app_opened_at`) makes the claim fail, so the fallback backs off — no race.
- The claimed row is handed to the hardened fallback, which either rewrites it into a DM challenge (preserving the query marker) or, on **any** error, declines + deletes — never a bare `return err`.
- A crashed claim (process dies after claim, before DM) leaves a `web_app_fallback_pending` row; the expired sweep recovers it (decline + delete).

## Task B1: migration — add `web_app_opened_at`

**Files:**
- Create: `resources/migrations/20260614120000-add-gatekeeper-challenge-opened-at.sql`

- [ ] **Step 1: Write the migration**

```sql
-- +migrate Up
ALTER TABLE gatekeeper_challenges
ADD COLUMN web_app_opened_at TIMESTAMP;

CREATE INDEX IF NOT EXISTS idx_gatekeeper_challenges_unopened_webapp
ON gatekeeper_challenges(created_at)
WHERE web_app_token <> '' AND web_app_opened_at IS NULL;

-- +migrate Down
DROP INDEX IF EXISTS idx_gatekeeper_challenges_unopened_webapp;

ALTER TABLE gatekeeper_challenges
DROP COLUMN web_app_opened_at;
```

Nullable, no default ⇒ existing rows and all non-WebApp challenges are `NULL` (= not opened), and excluded from the unopened sweep by `web_app_token <> ''`. The partial index keeps the 3s sweep cheap.

- [ ] **Step 2: Verify the migration applies**

Run: `go test ./internal/db/sqlite/ -run TestMigrations -v`
Expected: PASS (the existing migration test harness applies up/down cleanly; if there is no such test, run `go test ./internal/db/sqlite/...`).

- [ ] **Step 3: Commit**

```bash
git add resources/migrations/20260614120000-add-gatekeeper-challenge-opened-at.sql
git commit -m "feat(gatekeeper): add web_app_opened_at column for fast fallback"
```

---

## Task B2: entity field + transient status const

**Files:**
- Modify: `internal/db/entities.go` — `Challenge` struct (70-85), status consts (151-152)

- [ ] **Step 1: Add the field**

Add `"database/sql"` to imports, then add to the `Challenge` struct after `ExpiresAt`:

```go
		WebAppOpenedAt sql.NullTime `db:"web_app_opened_at"`
```

- [ ] **Step 2: Add the transient status const**

In the status const block (151-152):

```go
	ChallengeStatusWebAppFallbackPending   = "web_app_fallback_pending"
```

- [ ] **Step 3: Verify build**

Run: `go build ./internal/db/...`
Expected: compiles (no consumers yet).

- [ ] **Step 4: Commit**

```bash
git add internal/db/entities.go
git commit -m "feat(gatekeeper): add WebAppOpenedAt field and fallback-pending status"
```

---

## Task B3: sqlite layer — column in all queries + 3 new methods

**Files:**
- Modify: `internal/db/sqlite/client_challenges.go`

- [ ] **Step 1: Add `web_app_opened_at` to every challenge query**

In each SELECT column list (`GetChallengeByMessage`, `GetChallengeByWebAppToken`, `GetChallengeByChatUser`, `GetPassedJoinRequestChallengeByChatUser`, `GetExpiredChallenges`) append `, web_app_opened_at` to the selected columns.

In `CreateChallenge`: add `web_app_opened_at` to the INSERT column list and a `?` placeholder, add `challenge.WebAppOpenedAt` to the args, and add `web_app_opened_at = excluded.web_app_opened_at` to the `ON CONFLICT DO UPDATE SET` list.

In `UpdateChallenge`: add `web_app_opened_at = ?` to the SET list and `challenge.WebAppOpenedAt` to the args (positioned consistently with the column order). Because all SELECTs now read the real value, read-modify-`UpdateChallenge` cycles round-trip it correctly.

- [ ] **Step 2: Add `MarkWebAppChallengeOpened`**

```go
func (c *sqliteClient) MarkWebAppChallengeOpened(ctx context.Context, token string, openedAt time.Time) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	_, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET web_app_opened_at = ?
		WHERE web_app_token = ? AND web_app_token <> ''
			AND status = ?
			AND web_app_opened_at IS NULL
	`, openedAt, token, db.ChallengeStatusPending)
	return err
}
```

Idempotent: concurrent GETs (page reload, favicon) collapse to one write.

- [ ] **Step 3: Add `ClaimWebAppChallengeForFallback`**

```go
func (c *sqliteClient) ClaimWebAppChallengeForFallback(ctx context.Context, commChatID, userID, chatID int64) (bool, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	res, err := c.db.ExecContext(ctx, `
		UPDATE gatekeeper_challenges
		SET status = ?
		WHERE comm_chat_id = ? AND user_id = ? AND chat_id = ?
			AND status = ?
			AND web_app_token <> ''
			AND join_request_query_id <> ''
			AND web_app_opened_at IS NULL
	`, db.ChallengeStatusWebAppFallbackPending, commChatID, userID, chatID, db.ChallengeStatusPending)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}
```

- [ ] **Step 4: Add `GetUnopenedWebAppChallenges`**

```go
func (c *sqliteClient) GetUnopenedWebAppChallenges(ctx context.Context, deadline time.Time) ([]*db.Challenge, error) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	var challenges []*db.Challenge
	err := c.db.SelectContext(ctx, &challenges, `
		SELECT comm_chat_id, user_id, chat_id, status, success_uuid, web_app_token, join_request_query_id, captcha_prompt,
			captcha_options_json, join_message_id, challenge_message_id, attempts, created_at, expires_at, web_app_opened_at
		FROM gatekeeper_challenges
		WHERE web_app_token <> ''
			AND join_request_query_id <> ''
			AND status = ?
			AND web_app_opened_at IS NULL
			AND created_at <= ?
	`, db.ChallengeStatusPending, deadline)
	return challenges, err
}
```

`deadline` is passed as `now - webAppOpenDeadline` by the caller.

- [ ] **Step 5: Verify build**

Run: `go build ./internal/db/...`
Expected: compiles. (`*sqliteClient` will fail to satisfy `db.Client` until Task B4 adds the interface methods — that error appears at the consumer; that is expected and fixed next.)

- [ ] **Step 6: Commit**

```bash
git add internal/db/sqlite/client_challenges.go
git commit -m "feat(gatekeeper): add opened-at column to queries and claim/mark/sweep methods"
```

---

## Task B4: interfaces + mock store methods

**Files:**
- Modify: `internal/db/dependencies.go` (`Client` interface, near 25-32)
- Modify: `internal/handlers/chat/gatekeeper.go` (`gatekeeperStore`, 89-103)
- Modify: `internal/handlers/chat/gatekeeper_join_flow_test.go` (mock store, after line 153)

- [ ] **Step 1: Add to `db.Client` interface** (after `GetExpiredChallenges`):

```go
	MarkWebAppChallengeOpened(ctx context.Context, token string, openedAt time.Time) error
	ClaimWebAppChallengeForFallback(ctx context.Context, commChatID, userID, chatID int64) (bool, error)
	GetUnopenedWebAppChallenges(ctx context.Context, deadline time.Time) ([]*Challenge, error)
```

- [ ] **Step 2: Add to `gatekeeperStore` interface** (after `GetExpiredChallenges`):

```go
	MarkWebAppChallengeOpened(ctx context.Context, token string, openedAt time.Time) error
	ClaimWebAppChallengeForFallback(ctx context.Context, commChatID, userID, chatID int64) (bool, error)
	GetUnopenedWebAppChallenges(ctx context.Context, deadline time.Time) ([]*db.Challenge, error)
```

- [ ] **Step 3: Implement on the mock `gatekeeperFlowStore`** (mirror the SQL guards exactly so tests exercise real semantics):

```go
func (s *gatekeeperFlowStore) MarkWebAppChallengeOpened(_ context.Context, token string, openedAt time.Time) error {
	for _, challenge := range s.challenges {
		if challenge.WebAppToken == token && challenge.WebAppToken != "" &&
			challenge.Status == db.ChallengeStatusPending && !challenge.WebAppOpenedAt.Valid {
			challenge.WebAppOpenedAt = sql.NullTime{Time: openedAt, Valid: true}
		}
	}
	return nil
}

func (s *gatekeeperFlowStore) ClaimWebAppChallengeForFallback(_ context.Context, commChatID, userID, chatID int64) (bool, error) {
	challenge, ok := s.challenges[s.challengeKey(commChatID, userID, chatID)]
	if !ok {
		return false, nil
	}
	if challenge.Status != db.ChallengeStatusPending || challenge.WebAppToken == "" ||
		challenge.JoinRequestQueryID == "" || challenge.WebAppOpenedAt.Valid {
		return false, nil
	}
	challenge.Status = db.ChallengeStatusWebAppFallbackPending
	return true, nil
}

func (s *gatekeeperFlowStore) GetUnopenedWebAppChallenges(_ context.Context, deadline time.Time) ([]*db.Challenge, error) {
	out := make([]*db.Challenge, 0)
	for _, challenge := range s.challenges {
		if challenge.WebAppToken != "" && challenge.JoinRequestQueryID != "" &&
			challenge.Status == db.ChallengeStatusPending && !challenge.WebAppOpenedAt.Valid &&
			!challenge.CreatedAt.After(deadline) {
			out = append(out, cloneChallenge(challenge))
		}
	}
	return out, nil
}
```

Add `"database/sql"` to the test file imports if needed. Ensure `cloneChallenge` copies `WebAppOpenedAt` (it copies the whole struct by value if it does `clone := *challenge`; verify and, if it copies field-by-field, add the new field).

- [ ] **Step 4: Verify build**

Run: `go build ./... && go vet ./internal/...`
Expected: compiles; both `*sqliteClient` and `*gatekeeperFlowStore` now satisfy their interfaces.

- [ ] **Step 5: Commit**

```bash
git add internal/db/dependencies.go internal/handlers/chat/gatekeeper.go internal/handlers/chat/gatekeeper_join_flow_test.go
git commit -m "feat(gatekeeper): wire claim/mark/sweep methods through store interfaces and mock"
```

---

## Task B5: record the open signal on the GET page

**Files:**
- Modify: `internal/handlers/chat/gatekeeper_webapp.go` — `handleJoinCaptcha` (~808-818)
- Test: `internal/handlers/chat/gatekeeper_webapp_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestHandleJoinCaptchaMarksOpened(t *testing.T) {
	t.Parallel()

	store := newGatekeeperFlowStore()
	challenge := newWebAppChallenge(time.Now().Add(3 * time.Minute))
	_, _ = store.CreateChallenge(context.Background(), challenge)
	g := newWebAppTestGatekeeper(t, newTestBotAPI(t, func(string, *http.Request) any { return nil }), store)

	rr := getJoinCaptchaPage(t, g, challenge.WebAppToken) // existing helper invoking handleJoinCaptcha GET

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	got := store.onlyChallenge(t)
	if !got.WebAppOpenedAt.Valid {
		t.Fatal("expected web_app_opened_at to be set after page load")
	}
	if !got.ExpiresAt.Equal(challenge.ExpiresAt) {
		t.Fatal("expected solve timeout (ExpiresAt) to be unchanged by opening")
	}
}
```

Mirror the existing GET-page test setup (grep `handleJoinCaptcha(` in the test file). Note `newWebAppChallenge` must be fixed in Task B7-step-0 to use a real `CommChatID`; if running B5 first, set `challenge.CommChatID = 9001` in the test.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/chat/ -run TestHandleJoinCaptchaMarksOpened -v`
Expected: FAIL — opened-at not set.

- [ ] **Step 3: Implement**

In `handleJoinCaptcha`, after the challenge is confirmed pending+unexpired and after `decodeWebAppCaptchaData` succeeds (~818, before building the page), add:

```go
	if !challenge.WebAppOpenedAt.Valid {
		if err := g.store.MarkWebAppChallengeOpened(r.Context(), challenge.WebAppToken, time.Now()); err != nil {
			g.getLogEntry().WithField("error", err.Error()).Warn("failed to mark web app challenge opened")
		}
	}
```

Failure is non-fatal (warn + still render): worst case the user gets a premature DM fallback, which is recoverable.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/handlers/chat/ -run TestHandleJoinCaptchaMarksOpened -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/chat/gatekeeper_webapp.go internal/handlers/chat/gatekeeper_webapp_test.go
git commit -m "feat(gatekeeper): record web app open on captcha page load"
```

---

## Task B6: open-deadline constant

**Files:**
- Modify: `internal/handlers/chat/gatekeeper_challenge_service.go:20`

- [ ] **Step 1: Add the constant**

Next to `approvedJoinRequestChallengeTTL`:

```go
const webAppOpenDeadline = 11 * time.Second
```

- [ ] **Step 2: Commit** (compiles; consumed in B8)

```bash
git add internal/handlers/chat/gatekeeper_challenge_service.go
git commit -m "feat(gatekeeper): add web app open deadline constant"
```

---

## Task B7: hardened, atomic unified fallback (C2, C4, C6, C8, N12)

**Files:**
- Modify: `internal/handlers/chat/gatekeeper_scheduler.go` — replace `fallbackExpiredWebAppChallenge` (195-241) with `attemptWebAppFallback` + `fallbackClaimedWebAppChallenge`
- Modify: `internal/handlers/chat/gatekeeper_webapp_test.go` — fix the stale `newWebAppChallenge` helper
- Test: `internal/handlers/chat/gatekeeper_join_flow_test.go`

- [ ] **Step 0: Fix the stale test helper (C8)**

In `gatekeeper_webapp_test.go:831-845`, change `CommChatID: 0` to `CommChatID: 9001` so the helper produces a realistic WebApp challenge (private chat id), matching the committed `request.UserChatID` behavior.

- [ ] **Step 1: Write the failing tests**

Add to `gatekeeper_join_flow_test.go`:

```go
func TestAttemptWebAppFallbackDeclinesWhenTargetChatUnavailable(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		switch method {
		case testTelegramMethodGetChat:
			if r.Form.Get("chat_id") == "9001" {
				return map[string]any{"id": 9001, "type": "private", "first_name": "Neo"}
			}
			return apiError(http.StatusBadGateway, "Bad Gateway") // group getChat fails
		case testTelegramMethodJoinRequestQuery:
			return true // decline
		default:
			t.Fatalf("unexpected method: %s", method)
			return nil
		}
	})

	store := newGatekeeperFlowStore()
	ch := &db.Challenge{
		CommChatID: 9001, UserID: 42, ChatID: -100123,
		Status: db.ChallengeStatusWebAppFallbackPending, // already claimed
		SuccessUUID: "u", WebAppToken: "tok", JoinRequestQueryID: "join-query",
		CreatedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(2 * time.Minute),
	}
	_, _ = store.CreateChallenge(context.Background(), ch)
	g := newSchedulerTestGatekeeper(t, botAPI, store) // mirror existing scheduler test setup

	err := g.fallbackClaimedWebAppChallenge(context.Background(), store.onlyChallenge(t), webAppSettings())
	if err == nil {
		t.Fatal("expected error from failed group getChat")
	}
	if len(store.challenges) != 0 {
		t.Fatalf("expected row declined+deleted, got %d rows", len(store.challenges))
	}
	if got := len(recorder.byMethod(testTelegramMethodJoinRequestQuery)); got != 1 {
		t.Fatalf("expected one decline, got %d", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/handlers/chat/ -run TestAttemptWebAppFallback -v`
Expected: FAIL — `fallbackClaimedWebAppChallenge` does not exist yet.

- [ ] **Step 3: Implement the replacement**

Delete `fallbackExpiredWebAppChallenge` (195-241) and add:

```go
func (g *Gatekeeper) attemptWebAppFallback(ctx context.Context, challenge *db.Challenge, settings *db.Settings) error {
	claimed, err := g.store.ClaimWebAppChallengeForFallback(ctx, challenge.CommChatID, challenge.UserID, challenge.ChatID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	challenge.Status = db.ChallengeStatusWebAppFallbackPending
	return g.fallbackClaimedWebAppChallenge(ctx, challenge, settings)
}

func (g *Gatekeeper) fallbackClaimedWebAppChallenge(ctx context.Context, challenge *db.Challenge, settings *db.Settings) error {
	privateChat, err := g.s.GetBot().GetChat(api.ChatInfoConfig{ChatConfig: api.ChatConfig{ChatID: challenge.CommChatID}})
	if err != nil {
		return errors.Join(errors.WithMessage(err, "get private chat for fallback"), g.declineWebAppChallenge(ctx, challenge))
	}

	targetChat, err := g.s.GetBot().GetChat(api.ChatInfoConfig{ChatConfig: api.ChatConfig{ChatID: challenge.ChatID}})
	if err != nil {
		return errors.Join(errors.WithMessage(err, "get target chat for fallback"), g.declineWebAppChallenge(ctx, challenge))
	}

	user := &api.User{
		ID:        challenge.UserID,
		FirstName: privateChat.FirstName,
		LastName:  privateChat.LastName,
		UserName:  privateChat.UserName,
	}
	if user.FirstName == "" && user.UserName == "" {
		user.FirstName = "friend"
	}

	if err := g.startChallenge(ctx, nil, user, &targetChat.Chat, challenge.CommChatID, challenge.CommChatID, settings); err != nil {
		return errors.Join(errors.WithMessage(err, "start dm fallback challenge"), g.declineWebAppChallenge(ctx, challenge))
	}

	fallbackChallenge, err := g.store.GetChallengeByChatUser(ctx, challenge.ChatID, challenge.UserID)
	if err != nil {
		return errors.Join(errors.WithMessage(err, "load dm fallback challenge"), g.declineWebAppChallenge(ctx, challenge))
	}
	if fallbackChallenge == nil || fallbackChallenge.CommChatID != challenge.CommChatID {
		return g.declineWebAppChallenge(ctx, challenge)
	}
	fallbackChallenge.JoinRequestQueryID = challenge.JoinRequestQueryID
	return g.store.UpdateChallenge(ctx, fallbackChallenge)
}
```

Key points:
- `startChallenge` upserts onto the same PK `(CommChatID, UserID, ChatID)`, rewriting the claimed `web_app_fallback_pending` row into a DM `pending` challenge with `web_app_token=''` (so it leaves both sweeps) and a fresh captcha.
- **Every** error exit routes through `declineWebAppChallenge` (decline + delete + failed-stat) — no bare `return err`, killing the C2 and C6 loops.
- The dead `CommChatID==0` branch (C8) is gone; the claim guarantees a real WebApp row.
- `errors` here is `github.com/pkg/errors`; for `errors.Join` use the `stderrors` alias added in Task A3. **Note:** if Phase A is not yet merged, add `stderrors "errors"` to `gatekeeper_scheduler.go` imports and replace `errors.Join(...)` with `stderrors.Join(...)` in this file.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/handlers/chat/ -run TestAttemptWebAppFallback -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/chat/gatekeeper_scheduler.go internal/handlers/chat/gatekeeper_webapp_test.go
git commit -m "feat(gatekeeper): atomic claim + hardened unified web app fallback"
```

---

## Task B8: route both sweeps through the claim + recover stuck claims

**Files:**
- Modify: `internal/handlers/chat/gatekeeper_scheduler.go` — `processExpiredChallenges` (120-193); add `processUnopenedWebAppChallenges`
- Test: `internal/handlers/chat/gatekeeper_join_flow_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestProcessUnopenedWebAppChallengeFallsBackEarly(t *testing.T) {
	t.Parallel()

	recorder := &botRequestRecorder{}
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		recorder.record(t, method, r)
		switch method {
		case testTelegramMethodGetChat:
			if r.Form.Get("chat_id") == "9001" {
				return map[string]any{"id": 9001, "type": "private", "first_name": "Neo"}
			}
			return map[string]any{"id": -100123, "type": "supergroup", "title": "Wave Club"}
		case testTelegramMethodSendMessage:
			return recorder.nextSendMessageResult()
		default:
			t.Fatalf("unexpected method: %s", method)
			return nil
		}
	})

	store := newGatekeeperFlowStore()
	ch := &db.Challenge{
		CommChatID: 9001, UserID: 42, ChatID: -100123, Status: db.ChallengeStatusPending,
		SuccessUUID: "u", WebAppToken: "tok", JoinRequestQueryID: "join-query",
		CaptchaPrompt: "poodle", CaptchaOptionsJSON: `{"locale":"en","options":[{"id":"u","symbol":"A"}]}`,
		CreatedAt: time.Now().Add(-30 * time.Second), // older than 11s open deadline
		ExpiresAt: time.Now().Add(2 * time.Minute),   // NOT expired
	}
	_, _ = store.CreateChallenge(context.Background(), ch)
	g := newSchedulerTestGatekeeper(t, botAPI, store)

	if err := g.processUnopenedWebAppChallenges(context.Background()); err != nil {
		t.Fatalf("processUnopenedWebAppChallenges: %v", err)
	}
	got := store.onlyChallenge(t)
	if got.WebAppToken != "" {
		t.Fatalf("expected web app token cleared after early fallback, got %q", got.WebAppToken)
	}
	if got.JoinRequestQueryID != "join-query" {
		t.Fatalf("expected query marker preserved, got %q", got.JoinRequestQueryID)
	}
	if n := len(recorder.byMethod(testTelegramMethodSendMessage)); n != 1 {
		t.Fatalf("expected one DM challenge, got %d", n)
	}
}

func TestProcessUnopenedWebAppChallengeSkipsOpened(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		t.Fatalf("no telegram call expected when challenge already opened, got %s", method)
		return nil
	})
	store := newGatekeeperFlowStore()
	ch := &db.Challenge{
		CommChatID: 9001, UserID: 42, ChatID: -100123, Status: db.ChallengeStatusPending,
		WebAppToken: "tok", JoinRequestQueryID: "join-query",
		WebAppOpenedAt: sql.NullTime{Time: time.Now(), Valid: true},
		CreatedAt: time.Now().Add(-30 * time.Second), ExpiresAt: time.Now().Add(2 * time.Minute),
	}
	_, _ = store.CreateChallenge(context.Background(), ch)
	g := newSchedulerTestGatekeeper(t, botAPI, store)

	if err := g.processUnopenedWebAppChallenges(context.Background()); err != nil {
		t.Fatalf("processUnopenedWebAppChallenges: %v", err)
	}
	if store.onlyChallenge(t).WebAppToken == "" {
		t.Fatal("expected opened challenge to be left untouched")
	}
}
```

Also add a race-guard test: a row whose status was concurrently flipped to `passed_waiting_member_join` must NOT be claimed:

```go
func TestProcessUnopenedWebAppChallengeSkipsAlreadyPassed(t *testing.T) {
	t.Parallel()
	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		t.Fatalf("no telegram call expected for passed challenge, got %s", method)
		return nil
	})
	store := newGatekeeperFlowStore()
	ch := &db.Challenge{
		CommChatID: 9001, UserID: 42, ChatID: -100123,
		Status: db.ChallengeStatusPassedWaitingMemberJoin,
		WebAppToken: "tok", JoinRequestQueryID: "join-query",
		CreatedAt: time.Now().Add(-30 * time.Second), ExpiresAt: time.Now().Add(2 * time.Minute),
	}
	_, _ = store.CreateChallenge(context.Background(), ch)
	g := newSchedulerTestGatekeeper(t, botAPI, store)
	if err := g.processUnopenedWebAppChallenges(context.Background()); err != nil {
		t.Fatalf("err: %v", err)
	}
	if store.onlyChallenge(t).Status != db.ChallengeStatusPassedWaitingMemberJoin {
		t.Fatal("expected passed challenge untouched (claim must fail)")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/handlers/chat/ -run TestProcessUnopenedWebAppChallenge -v`
Expected: FAIL — `processUnopenedWebAppChallenges` does not exist.

- [ ] **Step 3: Implement `processUnopenedWebAppChallenges`**

Add to `gatekeeper_scheduler.go`:

```go
func (g *Gatekeeper) processUnopenedWebAppChallenges(ctx context.Context) error {
	entry := g.getLogEntry().WithField("method", "processUnopenedWebAppChallenges")

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	deadline := time.Now().Add(-webAppOpenDeadline)
	unopened, err := g.store.GetUnopenedWebAppChallenges(ctx, deadline)
	if err != nil {
		entry.WithField("error", err.Error()).Error("failed to get unopened web app challenges")
		return err
	}
	for _, challenge := range unopened {
		settings, err := g.fetchAndValidateSettings(ctx, challenge.ChatID)
		if err != nil {
			entry.WithField("error", err.Error()).Error("failed to load chat settings for unopened challenge")
			continue
		}
		if !settings.GatekeeperEnabled || !settings.GatekeeperCaptchaEnabled {
			if err := g.declineWebAppChallenge(ctx, challenge); err != nil {
				entry.WithField("error", err.Error()).Error("failed to clean up unopened challenge for disabled gatekeeper")
			}
			continue
		}
		if err := g.attemptWebAppFallback(ctx, challenge, settings); err != nil {
			entry.WithField("error", err.Error()).Error("failed to fall back unopened web app challenge")
		}
	}
	return nil
}
```

- [ ] **Step 4: Update `processExpiredChallenges` to use the claim + recover stuck claims**

In `processExpiredChallenges`, replace the WebApp branch (currently lines 152-157):

```go
		if challenge.Status == db.ChallengeStatusWebAppFallbackPending {
			if err := g.fallbackClaimedWebAppChallenge(ctx, challenge, settings); err != nil {
				entry.WithField("error", err.Error()).Error("failed to recover stuck web app fallback challenge")
			}
			continue
		}
		if challenge.WebAppToken != "" && challenge.JoinRequestQueryID != "" {
			if err := g.attemptWebAppFallback(ctx, challenge, settings); err != nil {
				entry.WithField("error", err.Error()).Error("failed to fallback expired web app challenge")
			}
			continue
		}
```

The first new branch recovers a row left in `web_app_fallback_pending` by a crash between claim and DM send: it is already claimed, so it goes straight to the hardened fallback (which will decline+delete on any error). The second branch handles an expired-but-still-pending WebApp challenge (e.g. the server was down during its open window) through the same atomic claim.

Note: `GetExpiredChallenges` returns `web_app_fallback_pending` rows because it filters only on `expires_at`. Confirm the `Status == db.ChallengeStatusPassedWaitingMemberJoin` branch (146) still precedes these so handoff cleanup is unaffected — it does.

- [ ] **Step 5: Run to verify they pass**

Run: `go test ./internal/handlers/chat/ -run 'TestProcessUnopenedWebAppChallenge|TestProcessExpired' -v`
Expected: PASS (new tests pass; the existing `TestProcessExpiredJoinRequestWebAppChallengeFallsBackToDM` still passes — it now flows through the claim, but assertions on token-cleared/query-kept hold).

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/chat/gatekeeper_scheduler.go internal/handlers/chat/gatekeeper_join_flow_test.go
git commit -m "feat(gatekeeper): early unopened-webapp sweep + claim-based expired fallback with crash recovery"
```

---

## Task B9: start the short-interval worker

**Files:**
- Modify: `internal/handlers/chat/gatekeeper.go` — interval const (60-63), `Start` worker loops (200-214)

- [ ] **Step 1: Add the interval constant**

In the const block (60-63):

```go
	processUnopenedWebAppInterval = 3 * time.Second
```

- [ ] **Step 2: Add the worker loop**

In `Start`, after the existing `processExpiredChallenges` worker `g.workerWG.Go(func() { ... })` block (200-214), add a third worker:

```go
	g.workerWG.Go(func() {
		ticker := time.NewTicker(processUnopenedWebAppInterval)
		defer ticker.Stop()

		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := g.processUnopenedWebAppChallenges(runCtx); err != nil && !errors.Is(err, context.Canceled) {
					g.getLogEntry().WithField("error", err.Error()).Error("failed to process unopened web app challenges")
				}
			}
		}
	})
```

Worst-case fast-fallback latency ≈ `webAppOpenDeadline (11s) + processUnopenedWebAppInterval (3s) = 14s`. Both are tunable constants.

- [ ] **Step 3: Verify build + full suite**

Run: `go build ./... && go test ./internal/handlers/chat/... -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/handlers/chat/gatekeeper.go
git commit -m "feat(gatekeeper): run unopened web app fallback sweep every 3s"
```

---

## Task B10: race regression test (C3 / N5)

**Files:**
- Test: `internal/handlers/chat/gatekeeper_join_flow_test.go`

- [ ] **Step 1: Write the test**

Simulate the interleaving deterministically: claim is attempted on a row that a "concurrent POST" already flipped to `passed_waiting_member_join`. The claim must fail and no fallback/ban must occur.

```go
func TestExpiredWebAppFallbackDoesNotClobberConcurrentlyApproved(t *testing.T) {
	t.Parallel()

	botAPI := newTestBotAPI(t, func(method string, r *http.Request) any {
		if method == testTelegramMethodGetChat {
			return map[string]any{"id": -100123, "type": "supergroup", "title": "Wave Club"}
		}
		t.Fatalf("no fallback telegram calls expected for approved row, got %s", method)
		return nil
	})

	store := newGatekeeperFlowStore()
	ch := &db.Challenge{
		CommChatID: 9001, UserID: 42, ChatID: -100123,
		Status: db.ChallengeStatusPassedWaitingMemberJoin, // POST won the race
		WebAppToken: "tok", JoinRequestQueryID: "join-query",
		CreatedAt: time.Now().Add(-5 * time.Minute), ExpiresAt: time.Now().Add(-time.Minute),
	}
	_, _ = store.CreateChallenge(context.Background(), ch)
	g := newSchedulerTestGatekeeper(t, botAPI, store)

	if err := g.processExpiredChallenges(context.Background()); err != nil {
		t.Fatalf("processExpiredChallenges: %v", err)
	}
	// passed_waiting_member_join + expired hits the non-punitive cleanup branch (line 146), never fallback/ban.
	if len(store.challenges) != 0 {
		t.Fatalf("expected passed-but-expired handoff row cleaned without penalty, got %d", len(store.challenges))
	}
}
```

- [ ] **Step 2: Run**

Run: `go test ./internal/handlers/chat/ -run TestExpiredWebAppFallbackDoesNotClobberConcurrentlyApproved -v`
Expected: PASS (the passed-status branch at scheduler.go:146 handles it; the claim would also refuse). This locks in the C3/N5 fix.

- [ ] **Step 3: Commit**

```bash
git add internal/handlers/chat/gatekeeper_join_flow_test.go
git commit -m "test(gatekeeper): lock in race-safety of claim-based fallback"
```

---

## Phase B checkpoint — full verification

- [ ] Run the project's required checks:

Run: `go test ./... && go vet ./...`
Expected: PASS

Run: `go tool golangci-lint run --enable=unused --enable=unparam --enable=ineffassign --enable=goconst ./internal/handlers/chat/... ./internal/db/...`
Expected: no findings (remove any now-unused symbols, e.g. confirm the old `fallbackExpiredWebAppChallenge` is fully deleted).

- [ ] Manual smoke (optional, prod-like): submit a real join request from a client that does not render the Mini App; confirm via logs that `processUnopenedWebAppChallenges` fires a DM challenge ~14s later (not ~3-4 min), the query marker is preserved, and solving the DM CAPTCHA approves the join.

---

## Self-Review (completed during authoring)

1. **Spec coverage:** Every recommended fix except the two explicitly-deferred judgment calls (audit-point-9, N7-loopback) maps to a task: C1→A1, C5→A2, C7→A3, N1→A4, N2→A5, N10→A6, C2/C6→B7, C8→B7-step0+B7, C3/N5→B7+B8+B10, C4/N12→B7, C10→B1–B9.
2. **Placeholder scan:** No TBD/“add error handling” placeholders; every code step shows real code. Test helper names (`newWebAppTestGatekeeper`, `postJoinCaptchaAnswer`, `getJoinCaptchaPage`, `newSchedulerTestGatekeeper`, `apiError`) must be matched to the actual helpers already in the test files — grep before writing; if a helper does not exist under that name, reuse the existing setup idiom from the nearest existing test (the assertions are what matter).
3. **Type consistency:** `WebAppOpenedAt sql.NullTime`, `ClaimWebAppChallengeForFallback(...) (bool, error)`, `GetUnopenedWebAppChallenges(ctx, deadline) ([]*db.Challenge, error)`, `MarkWebAppChallengeOpened(ctx, token, openedAt)`, status const `ChallengeStatusWebAppFallbackPending`, funcs `attemptWebAppFallback`/`fallbackClaimedWebAppChallenge`, consts `webAppOpenDeadline`/`processUnopenedWebAppInterval`/`joinCaptchaInitDataTTL` — names are used identically across all tasks.
