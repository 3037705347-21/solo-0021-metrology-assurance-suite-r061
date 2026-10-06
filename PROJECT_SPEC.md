# Metrology Assurance Suite

## Project Goal

Metrology Assurance Suite is a local HTTP service that keeps a traceable record
of laboratory measurement assurance work. It registers devices, opens a
verification case for one device, defines the case measurement plan with one or
more checkpoints at opening, records technician results point by point,
requests a repeat measurement when any checkpoint exceeds tolerance, and seals
the complete evidence record only after every checkpoint is accepted.

The service is intentionally self-contained. It uses an in-memory repository
with atomic copy-on-write mutations and does not require a database or an
external service.

## Users

- Laboratory steward: registers devices and opens verification cases.
- Measurement technician: records observed values for an open case.
- Quality verifier: reviews accepted measurements and seals the case.

## Core Entities

- Device: a laboratory asset with a unique asset tag, model, room, steward, and
  lifecycle state.
- VerificationCase: one bounded assurance activity for a device, with its
  checkpoint plan, current aggregate state, version, and assigned steward.
- Checkpoint: one required measurement point defined when the case is opened.
  It has an ordinal, label, nominal value, tolerance, status
  (`pending`/`accepted`/`rejected`), and the measurement that is its current
  basis.
- Measurement: an observed value for one checkpoint, computed deviation,
  acceptance decision, technician, and timestamp. Every attempt, including
  rejected ones, is retained as case history.
- EvidenceSeal: the immutable closing record for a reviewed case, including a
  frozen basis snapshot for every checkpoint and the selected accepted
  measurement with the smallest absolute deviation.
- AuditEvent: an append-only trace of meaningful case and device changes.

## Workflows

### 1. Register Device

The steward sends `POST /devices` with an asset tag, model, room, and steward.
The service validates the request, normalizes the asset tag, rejects duplicate
tags, and creates an active device.

### 2. Open Verification Case

The steward sends `POST /cases` with a device ID, steward, and the measurement
plan. A plan is an ordered list of checkpoints, each with a label, nominal
value, and tolerance. A legacy request with one case-level nominal and
tolerance is accepted as a single-point plan with a default checkpoint. The
service verifies that the device exists, is active, and has no unfinished case;
it also rejects mixing case-level nominal/tolerance with a plan, duplicate
point labels, and non-positive tolerances. A valid request creates a case in
`awaiting_measurement` with all points `pending`.

### 3. Record Point Measurement

A technician sends `POST /cases/{id}/measurements` with a `checkpoint_id`,
observed value, and technician. The checkpoint id is optional only for a
single-point case. The case must be `awaiting_measurement`, and the target
checkpoint must be `pending`; an already closed point, an unknown point, or a
sealed case is rejected. The service computes the signed deviation from the
point's nominal value, stores the measurement as the point basis, and
recomputes the aggregate case state:

- a rejected point moves the case to `recheck_required` and blocks every other
  point until the case is reopened;
- accepting a point while others remain pending keeps the case in
  `awaiting_measurement`;
- accepting the last pending point moves the case to `review_ready`.

### 4. Reopen Verification Case

The steward sends `POST /cases/{id}/reopen` after one or more points were
rejected. Only `recheck_required` cases may move back to
`awaiting_measurement`. The service resets exactly the rejected points to
`pending` and clears their current basis so they can be measured again;
accepted points and every rejected measurement in history are preserved. The
actor, reason, and reset point labels are recorded in the audit trail.

### 5. Seal Verification

The verifier sends `POST /cases/{id}/seal` after all points are accepted. Only
a `review_ready` case with every checkpoint accepted may be sealed. The service
selects the accepted measurement with the smallest absolute deviation, freezes
one basis snapshot per checkpoint into the evidence seal, creates exactly one
seal, moves the case to `sealed`, and rejects later measurements, reopens, and
seal attempts.

## State And Rules

Device state:

- `active -> retired`
- `retired` is terminal.

VerificationCase state (aggregated from its checkpoints):

- `awaiting_measurement -> review_ready` when the last pending point is
  accepted;
- `awaiting_measurement -> recheck_required` when any point is rejected;
- `awaiting_measurement -> awaiting_measurement` when a point is accepted but
  others remain pending (version still increments);
- `recheck_required -> awaiting_measurement` on reopen;
- `review_ready -> sealed`;
- `sealed` is terminal.

Checkpoint state:

- `pending -> accepted` on an in-tolerance measurement;
- `pending -> rejected` on an out-of-tolerance measurement;
- `rejected -> pending` on case reopen (accepted points do not move);
- an accepted or rejected point cannot be recorded again directly.

Required invariants:

- Asset tags are unique after case-insensitive normalization.
- A device has at most one unfinished verification case.
- A case has at least one checkpoint; labels are unique within a plan.
- Every point tolerance must be positive; point nominals must be finite.
- Measurements are accepted only while the case is awaiting measurement and
  the target checkpoint is pending.
- A rejected point blocks the whole case; other pending points cannot close.
- A case becomes review ready only when every checkpoint is accepted.
- Reopen resets only rejected points; accepted point bases stay intact.
- A case can be sealed only when every checkpoint is accepted.
- A case can have at most one evidence seal.
- A sealed case cannot accept measurements, reopen, or seal again.
- Every measurement attempt stays in case history, including rejected and
  retried points; the seal freezes the current basis of every checkpoint.
- Every mutation that changes a case also appends an audit event in the same
  repository commit.

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
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events`

## Validation Plan

The baseline build command is `go build ./...`; unit and integration tests run
with `go test ./...` (including `-race` for the concurrency paths). Each
declared workflow has a bounded production smoke command under
`cmd/workflowcheck`. The command starts an HTTP test server in the same
process, calls the public routes, and verifies both success and failure
behavior. The seal check races concurrent seal requests, and the multi-point
seal check additionally races concurrent records of the final pending point,
to prove that the repository mutation lock permits exactly one closing record
and one seal.

Multi-point smoke workflows:

- `open-multi-point-case`: plan definition, ordering, and plan validation.
- `record-point-results`: point-by-point records, duplicate and unknown point
  rejection, blocked points after a failure, and early seal rejection.
- `retest-rejected-point`: reopen resets only rejected points, accepted bases
  and rejected history survive, supplementary results close the case.
- `seal-multi-point-case`: concurrent final-point race, all-points seal, frozen
  per-point bases, and post-seal immutability.
