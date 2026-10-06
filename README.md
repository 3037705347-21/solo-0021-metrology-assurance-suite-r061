# Metrology Assurance Suite

Metrology Assurance Suite is a Go HTTP service for bounded laboratory
measurement assurance work. It registers devices, opens verification cases
around a measurement plan of one or more checkpoints (one per range point,
each with its own nominal value and tolerance), records technician results
point by point, handles failed points through guarded rework, and seals the
whole traceable evidence package only after every checkpoint passes.

## Run

```bash
go run ./cmd/metrology-service --addr 127.0.0.1:18121
```

The service listens on `127.0.0.1:18121` by default.

## Build And Verify

```bash
go build ./...
go run ./cmd/workflowcheck --workflow register-device
go run ./cmd/workflowcheck --workflow open-verification-case
go run ./cmd/workflowcheck --workflow record-measurement
go run ./cmd/workflowcheck --workflow reopen-verification-case
go run ./cmd/workflowcheck --workflow seal-verification
go run ./cmd/workflowcheck --workflow open-multi-checkpoint-case
go run ./cmd/workflowcheck --workflow record-checkpoint-results
go run ./cmd/workflowcheck --workflow rework-checkpoint
go run ./cmd/workflowcheck --workflow seal-checkpoint-package
```

The workflow runner starts a fresh in-process HTTP service and exercises the
public API. It is a production smoke command, not a unit test.

The multi-point workflows open a case with several checkpoints, close points
one at a time (including a duplicate-record rejection and a same-point race
where exactly one request wins), fail a point, reopen so only the failed point
is reset while passed evidence survives, and finally reject premature sealing
before freezing the complete package under a concurrent seal race.

Tests are intentionally deferred for this initialization baseline. The later
engineering-task stage will add the unit and integration test suite and replace
or extend these smoke checks with red/green verification tests.

## API

- `GET /healthz`
- `POST /devices`
- `GET /devices/{id}`
- `POST /cases` (with either a `checkpoints` plan or a legacy `nominal`/`tolerance` pair)
- `GET /cases/{id}`
- `POST /cases/{id}/checkpoints`
- `POST /cases/{id}/measurements` (legacy single-point route)
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events`

## Layout

- `cmd/metrology-service`: executable HTTP server.
- `cmd/workflowcheck`: bounded workflow verification commands.
- `internal/httpapi`: route and JSON boundary.
- `internal/assurance`: workflow orchestration.
- `internal/domain`: state rules and invariants.
- `internal/repository`: copy-on-write in-memory persistence.
- `internal/contracts`: API request and response models.
- `internal/bootstrap`: concrete dependency wiring.

## Inputs And Outputs

Inputs are JSON HTTP requests. Outputs are JSON response envelopes with either a
resource view or a stable error object:

```json
{
  "error": {
    "code": "state_conflict",
    "message": "case is not awaiting measurement"
  }
}
```

The service has no environment variables and no external dependencies.
