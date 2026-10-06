# Metrology Assurance Suite

## Project Goal

Metrology Assurance Suite is a local HTTP service that keeps a traceable record
of laboratory measurement assurance work. It registers devices, opens a
verification case for one device around a measurement plan of one or more
checkpoints, records technician results point by point, requests repeat
measurement of failed points after repair, and seals the whole evidence
package only after every checkpoint passes review.

The service is intentionally self-contained. It uses an in-memory repository
with atomic copy-on-write mutations and does not require a database or an
external service.

## Users

- Laboratory steward: registers devices and opens verification cases with the
  checkpoint plan (one point per range point, each with its own nominal value
  and tolerance).
- Measurement technician: records observed values for one open checkpoint at a
  time.
- Quality verifier: reviews the closed package and seals the case.

## Core Entities

- Device: a laboratory asset with a unique asset tag, model, room, steward, and
  lifecycle state.
- VerificationCase: one bounded assurance activity for a device. It carries an
  ordered, immutable measurement plan of checkpoints, the aggregate state,
  version, and assigned steward.
- Checkpoint: one planned range point. It holds the point code, label, nominal
  value, tolerance, lifecycle status (`pending`/`passed`/`failed`), attempt
  count, and the latest decision's measurement reference.
- Measurement: an observed value for one checkpoint, computed deviation,
  acceptance decision, technician, and timestamp. Every attempt (including
  failed attempts that were later reworked) is retained.
- EvidenceSeal: the immutable closing record for a reviewed case. It freezes
  the selected overall accepted measurement and one SealedCheckpoint per
  planned point, each citing the measurement that closed it.
- AuditEvent: an append-only trace of meaningful case and device changes.

## Workflows

### 1. Register Device

The steward sends `POST /devices` with an asset tag, model, room, and steward.
The service validates the request, normalizes the asset tag, rejects duplicate
tags, and creates an active device.

### 2. Open Verification Case

The steward sends `POST /cases` with a device ID, steward, and either:

- a `checkpoints` array defining the measurement plan, each element carrying a
  `code`, optional `label`, `nominal`, and `tolerance`; or
- the legacy single-point `nominal` and `tolerance` pair (synthesized into one
  checkpoint with code `default`).

Codes default to `cp-01`, `cp-02`, ... and labels default to the code. Codes
and labels must be unique within a plan; every tolerance must be positive. The
service verifies that the device exists, is active, and has no unfinished
case. A valid request creates a case in `awaiting_measurement` with every
checkpoint pending.

### 3. Record Checkpoint Result

A technician sends `POST /cases/{id}/checkpoints` with a checkpoint `code`,
`observed` value, and `technician`. The case must not be sealed, and it must
not be in `recheck_required` (the steward reopens first to authorize rework).
The point must be part of the plan and still be `pending`; a point that has a
decision rejects a repeat record with `checkpoint_already_recorded`. The
nominal value and tolerance always come from the plan, never from the
request. The case stays `awaiting_measurement` while points remain open, moves
to `review_ready` once every point passes, and moves to `recheck_required` as
soon as one point fails.

The legacy `POST /cases/{id}/measurements` route records against the single
synthesized `default` checkpoint and is retained for single-point cases.

### 4. Reopen Verification Case

The steward sends `POST /cases/{id}/reopen` after an out-of-tolerance point.
Only `recheck_required` cases may move back to `awaiting_measurement`. Only
the failed checkpoints are reset to pending; passed points keep their decision
and measurement reference. Failed measurements stay in the history, and each
point keeps its cumulative attempt count. The service records the actor and
reason in the audit trail.

### 5. Seal Verification

The verifier sends `POST /cases/{id}/seal` after every checkpoint has an
accepted measurement. Sealing while any point is pending or failed is
rejected. The service selects the accepted measurement with the smallest
absolute deviation as the overall reference, freezes one sealed checkpoint
per planned point (requirements, result, attempts, technician, and
measurement id), creates exactly one evidence seal, moves the case to
`sealed`, and rejects later mutations.

## State And Rules

Device state:

- `active -> retired`
- `retired` is terminal.

Checkpoint state:

- `pending -> passed`
- `pending -> failed`
- `failed -> pending` (only through case reopen; evidence history retained)
- `passed` is never reset by rework.

VerificationCase state is derived from the checkpoint decisions:

- `awaiting_measurement -> review_ready` when every checkpoint passes.
- `awaiting_measurement -> recheck_required` when any checkpoint fails.
- `recheck_required -> awaiting_measurement` via reopen (failed points reset).
- `review_ready -> sealed`.
- `sealed` is terminal.

Required invariants:

- Asset tags are unique after case-insensitive normalization.
- A device has at most one unfinished verification case.
- A measurement plan defines at least one checkpoint; codes and labels are
  unique; every tolerance is positive.
- A result is accepted only against the point's own plan limits; technicians
  cannot supply or alter nominal values or tolerances.
- A pending checkpoint accepts exactly one result until reset; a failed point
  blocks the whole case until the steward reopens it.
- The case can be sealed only when every checkpoint is `passed`; a seal
  freezes a decision and measurement reference for every planned point.
- A case can have at most one evidence seal.
- A sealed case cannot accept measurements, reopen, or seal again.
- Every measurement attempt is retained for traceability, including failed
  attempts of points that later pass; each checkpoint accumulates its attempt
  count.
- Every mutation that changes a case also appends an audit event in the same
  repository commit, so the repository mutation lock makes repeat records,
  premature seals, and concurrent sealers deterministic.

## Modules

- `cmd/metrology-service`: process entrypoint and graceful HTTP shutdown.
- `cmd/workflowcheck`: bounded smoke checks that call the public HTTP API.
- `internal/httpapi`: routing, request decoding, and stable error mapping.
- `internal/assurance`: workflow orchestration and atomic decisions.
- `internal/domain`: entities, validation, state transitions, and errors.
- `internal/repository`: mutex-protected in-memory persistence with copy-on-write
  commits.
- `internal/contracts`: request and response contracts shared by the HTTP layer.

## Public Interfaces

- `GET /healthz`
- `POST /devices`
- `GET /devices/{id}`
- `POST /cases`
- `GET /cases/{id}`
- `POST /cases/{id}/checkpoints`
- `POST /cases/{id}/measurements` (legacy single-point route)
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events`

## Validation Plan

The baseline build command is `go build ./...`. Each declared workflow has a
bounded production smoke command under `cmd/workflowcheck`. The command starts
an HTTP test server in the same process, calls the public routes, and verifies
both success and failure behavior. The checkpoint check races two technicians
against the same pending point (exactly one wins) and records distinct points
in parallel; the seal check races two concurrent seal requests to prove that
the repository mutation lock permits exactly one seal.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add unit and integration tests, including red/green
verification for the workflows and their failure paths.
