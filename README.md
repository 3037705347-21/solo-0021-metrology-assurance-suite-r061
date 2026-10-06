# Metrology Assurance Suite

Metrology Assurance Suite is a Go HTTP service for bounded laboratory
measurement assurance work. It registers devices, opens verification cases
with a multi-checkpoint measurement plan, records technician results point by
point, handles failed tolerance decisions with targeted retests, and seals one
traceable evidence package only after every checkpoint is accepted.

## Run

```bash
go run ./cmd/metrology-service --addr 127.0.0.1:18121
```

The service listens on `127.0.0.1:18121` by default.

## Build And Verify

```bash
go build ./...
go test ./...
go run ./cmd/workflowcheck --workflow register-device
go run ./cmd/workflowcheck --workflow open-verification-case
go run ./cmd/workflowcheck --workflow record-measurement
go run ./cmd/workflowcheck --workflow reopen-verification-case
go run ./cmd/workflowcheck --workflow seal-verification
go run ./cmd/workflowcheck --workflow open-multi-point-case
go run ./cmd/workflowcheck --workflow record-point-results
go run ./cmd/workflowcheck --workflow retest-rejected-point
go run ./cmd/workflowcheck --workflow seal-multi-point-case
```

The workflow runner starts a fresh in-process HTTP service and exercises the
public API. It is a production smoke command; the unit and integration tests
live next to the packages (`go test ./...`).

## API

- `GET /healthz`
- `POST /devices`
- `GET /devices/{id}`
- `POST /cases`
- `GET /cases/{id}`
- `POST /cases/{id}/measurements`
- `POST /cases/{id}/reopen`
- `POST /cases/{id}/seal`
- `GET /cases/{id}/events`

## Measurement Plans And Checkpoints

A case is opened with an ordered plan; each checkpoint has its own nominal
value and tolerance:

```json
{
  "device_id": "device-0001",
  "steward": "Qiao",
  "checkpoints": [
    {"label": "10bar",  "nominal": 10.0,  "tolerance": 0.05},
    {"label": "50bar",  "nominal": 50.0,  "tolerance": 0.1},
    {"label": "100bar", "nominal": 100.0, "tolerance": 0.2}
  ]
}
```

The legacy single nominal/tolerance body still opens a one-point case. Point
results are recorded against a checkpoint:

```json
{"checkpoint_id": "checkpoint-0002", "observed": 50.03, "technician": "Rui"}
```

`checkpoint_id` is optional only for a single-point case. The case view lists
each checkpoint with its status and current basis measurement. A rejected
point moves the case to `recheck_required` and blocks the remaining points;
reopen resets only the rejected points for retest while keeping accepted
bases and the full measurement history. Sealing is possible only when all
points are accepted, and the seal freezes one basis snapshot per point.

Stable conflict codes include `checkpoint_closed` (recording an already closed
point), `checkpoints_open` (seal basis problems), and
`duplicate_checkpoint` (repeated labels in a plan).

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
