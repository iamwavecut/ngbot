# Context-aware vacancy moderation design

## Goal

Prevent genuine, detailed job vacancies from being classified as spam merely because they invite applicants to contact a recruiter, while continuing to catch vague job scams and actual gambling promotion.

## Decision boundary

A recruiter contact, phone number, Telegram username, application instruction, industry name, or `#vacancy` tag is not independent spam evidence. A detailed vacancy is benign when it identifies a real role or professional function and provides substantive duties, requirements, conditions, or hiring context. Employment at an iGaming company is distinct from advertising a casino.

Job-related spam still includes vague or anonymous income offers, hidden duties, unrealistic earnings, mass recruitment, passive-income or investment schemes, referral promotion, evasion through mixed alphabets, and requests to write `+` merely to reveal essential details.

When evidence is insufficient, the classifier returns non-spam.

## Per-chat context

Each chat has an LLM moderation profile:

- `general`: the existing default, with the corrected global vacancy boundary.
- `jobs_hr`: vacancies, recruiting, candidate discussions, and recruiter contacts are explicitly on-topic; scam signals remain enforceable.

The profile is selected by a chat administrator on a dedicated leaf screen in the existing LLM settings menu. Existing chats migrate to `general` without behavioral changes unrelated to the corrected boundary.

## Chat-specific examples

Existing chat examples remain labeled spam. A new classification column allows administrators to add safe examples as well. The same list/detail/add/delete workflow is reused, filtered by classification. The classifier receives both labels as structured, live, untrusted JSON.

## Data flow

The reactor loads the chat profile from `db.Settings`, loads up to 20 spam and 20 safe examples, normalizes the candidate text as before, and sends a structured classification context to the detector. Reaction-profile checks use the `general` profile and no chat examples.

The system prompt remains cacheable. Candidate text, profile selection, and administrator examples remain live data and cannot add privileged instructions.

## Verification

Regression tests cover the two supplied vacancy families, the CTA boundary, iGaming employment versus casino advertising, profile propagation, labeled example persistence and filtering, admin state synchronization, migration up/down behavior, and translation completeness. The repository's Go tests, race tests, shuffle tests, vet, lint, module diff, Docker build, release checks, production revision, migrations, container health, fresh logs, and a bounded live Gemini evaluation form the delivery gate.
