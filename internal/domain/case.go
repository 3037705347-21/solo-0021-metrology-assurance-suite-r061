package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
)

type CaseState string

const (
	CaseAwaitingMeasurement CaseState = "awaiting_measurement"
	CaseReviewReady         CaseState = "review_ready"
	CaseRecheckRequired     CaseState = "recheck_required"
	CaseSealed              CaseState = "sealed"
)

type VerificationCase struct {
	ID          string       `json:"id"`
	DeviceID    string       `json:"device_id"`
	Steward     string       `json:"steward"`
	Checkpoints []Checkpoint `json:"checkpoints"`
	State       CaseState    `json:"state"`
	Version     int          `json:"version"`
	OpenedAt    time.Time    `json:"opened_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	SealedAt    *time.Time   `json:"sealed_at,omitempty"`
}

// NewVerificationCase opens a case around a fixed measurement plan. The plan
// is built by the caller so that open-time validation failures never create a
// partial case.
func NewVerificationCase(id, deviceID string, plans []CheckpointPlan, steward string, at time.Time) (VerificationCase, error) {
	points, err := BuildCheckpoints(plans)
	if err != nil {
		return VerificationCase{}, err
	}
	item := VerificationCase{
		ID:          strings.TrimSpace(id),
		DeviceID:    strings.TrimSpace(deviceID),
		Steward:     strings.TrimSpace(steward),
		Checkpoints: points,
		State:       CaseAwaitingMeasurement,
		Version:     1,
		OpenedAt:    at.UTC(),
		UpdatedAt:   at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return VerificationCase{}, err
	}
	return item, nil
}

func (c VerificationCase) Validate() error {
	if c.ID == "" || c.DeviceID == "" || c.Steward == "" {
		return fmt.Errorf("%w: case id, device id, and steward are required", ErrInvalidInput)
	}
	if len(c.Checkpoints) == 0 {
		return fmt.Errorf("%w: measurement plan must define at least one checkpoint", ErrInvalidInput)
	}
	seenCodes := make(map[string]struct{}, len(c.Checkpoints))
	for index := range c.Checkpoints {
		point := &c.Checkpoints[index]
		if point.Order != index+1 {
			return fmt.Errorf("%w: checkpoint %s is out of order", ErrInvalidInput, point.Code)
		}
		if _, exists := seenCodes[point.Code]; exists {
			return fmt.Errorf("%w: duplicate checkpoint code %q", ErrInvalidInput, point.Code)
		}
		seenCodes[point.Code] = struct{}{}
		if err := point.Validate(); err != nil {
			return err
		}
	}
	if c.Version < 1 || c.OpenedAt.IsZero() || c.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: case version and timestamps are required", ErrInvalidInput)
	}
	switch c.State {
	case CaseAwaitingMeasurement, CaseReviewReady, CaseRecheckRequired, CaseSealed:
	default:
		return fmt.Errorf("%w: unsupported case state %q", ErrInvalidInput, c.State)
	}
	if c.State != CaseSealed {
		if derived := c.DerivedState(); derived != c.State {
			return fmt.Errorf("%w: case state %s does not match checkpoint decisions %s", ErrInvalidInput, c.State, derived)
		}
	}
	return nil
}

func (c VerificationCase) IsUnfinished() bool {
	return c.State != CaseSealed
}

// Checkpoint resolves a planned point by its stable code.
func (c VerificationCase) Checkpoint(code string) (*Checkpoint, bool) {
	code = strings.TrimSpace(code)
	for index := range c.Checkpoints {
		if c.Checkpoints[index].Code == code {
			return &c.Checkpoints[index], true
		}
	}
	return nil, false
}

// DecisionCounts summarizes how many points are still open and how the
// recorded points came out.
func (c VerificationCase) DecisionCounts() (total, pending, passed, failed int) {
	total = len(c.Checkpoints)
	for index := range c.Checkpoints {
		switch c.Checkpoints[index].Status {
		case CheckpointPending:
			pending++
		case CheckpointPassed:
			passed++
		case CheckpointFailed:
			failed++
		}
	}
	return total, pending, passed, failed
}

// AllPassed reports whether every planned point has an accepted measurement.
func (c VerificationCase) AllPassed() bool {
	for index := range c.Checkpoints {
		if c.Checkpoints[index].Status != CheckpointPassed {
			return false
		}
	}
	return true
}

// DerivedState computes the aggregate case state from point decisions. A
// single failed point blocks the whole package; once failures are retried the
// case stays open until every point passes.
func (c VerificationCase) DerivedState() CaseState {
	if len(c.Checkpoints) == 0 {
		return CaseAwaitingMeasurement
	}
	if c.AllPassed() {
		return CaseReviewReady
	}
	for index := range c.Checkpoints {
		if c.Checkpoints[index].Status == CheckpointFailed {
			return CaseRecheckRequired
		}
	}
	return CaseAwaitingMeasurement
}

// RecordCheckpoint stores one technician measurement against one planned
// point. A decided point or a point on a sealed case cannot be recorded
// again.
func (c *VerificationCase) RecordCheckpoint(pointCode string, measurement Measurement, at time.Time) (*Checkpoint, error) {
	if c.State == CaseSealed {
		return nil, fmt.Errorf("%w: case is sealed", ErrStateConflict)
	}
	// The steward must reopen a failed case and record the rework reason
	// before any point can be measured again; this gate comes before the
	// per-point duplicate guard so a technician cannot bypass rework.
	if c.State == CaseRecheckRequired {
		return nil, fmt.Errorf("%w: case must be reopened before another point can be measured", ErrStateConflict)
	}
	point, exists := c.Checkpoint(pointCode)
	if !exists {
		return nil, fmt.Errorf("%w: checkpoint %s", ErrCheckpointMissing, pointCode)
	}
	if !point.IsPending() {
		return nil, fmt.Errorf("%w: checkpoint %s is already %s", ErrCheckpointTaken, point.Code, point.Status)
	}
	point.record(measurement, at)
	c.State = c.DerivedState()
	c.Version++
	c.UpdatedAt = at.UTC()
	return point, nil
}

// Reopen returns a case with a failed point to measurement work. Only the
// failed points are reset, so passed points and their evidence cannot be
// fabricated away by rework.
func (c *VerificationCase) Reopen(at time.Time) (int, error) {
	if c.State != CaseRecheckRequired {
		return 0, fmt.Errorf("%w: only a recheck_required case can be reopened", ErrStateConflict)
	}
	reset := 0
	for index := range c.Checkpoints {
		if c.Checkpoints[index].Status == CheckpointFailed {
			c.Checkpoints[index].resetForRetry()
			reset++
		}
	}
	c.State = CaseAwaitingMeasurement
	c.Version++
	c.UpdatedAt = at.UTC()
	return reset, nil
}

func (c *VerificationCase) Seal(at time.Time) error {
	if c.State != CaseReviewReady {
		return fmt.Errorf("%w: every checkpoint must pass before sealing", ErrStateConflict)
	}
	if !c.AllPassed() {
		return fmt.Errorf("%w: every checkpoint must pass before sealing", ErrStateConflict)
	}
	stamp := at.UTC()
	c.State = CaseSealed
	c.Version++
	c.UpdatedAt = stamp
	c.SealedAt = &stamp
	return nil
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
