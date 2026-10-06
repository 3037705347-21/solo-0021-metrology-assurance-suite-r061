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
	ID        string     `json:"id"`
	DeviceID  string     `json:"device_id"`
	Nominal   float64    `json:"nominal"`
	Tolerance float64    `json:"tolerance"`
	Steward   string     `json:"steward"`
	State     CaseState  `json:"state"`
	Version   int        `json:"version"`
	OpenedAt  time.Time  `json:"opened_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	SealedAt  *time.Time `json:"sealed_at,omitempty"`
}

func NewVerificationCase(id, deviceID string, nominal, tolerance float64, steward string, at time.Time) (VerificationCase, error) {
	item := VerificationCase{
		ID:        strings.TrimSpace(id),
		DeviceID:  strings.TrimSpace(deviceID),
		Nominal:   nominal,
		Tolerance: tolerance,
		Steward:   strings.TrimSpace(steward),
		State:     CaseAwaitingMeasurement,
		Version:   1,
		OpenedAt:  at.UTC(),
		UpdatedAt: at.UTC(),
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
	if !isFinite(c.Nominal) || !isFinite(c.Tolerance) || c.Tolerance < 0 {
		return fmt.Errorf("%w: nominal must be finite and tolerance cannot be negative", ErrInvalidInput)
	}
	if c.Version < 1 || c.OpenedAt.IsZero() || c.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: case version and timestamps are required", ErrInvalidInput)
	}
	switch c.State {
	case CaseAwaitingMeasurement, CaseReviewReady, CaseRecheckRequired, CaseSealed:
		return nil
	default:
		return fmt.Errorf("%w: unsupported case state %q", ErrInvalidInput, c.State)
	}
}

func (c VerificationCase) IsUnfinished() bool {
	return c.State != CaseSealed
}

// advance recomputes the case state from its checkpoint plan after a point was
// recorded. A rejected point blocks the whole case; otherwise the case becomes
// review ready only once every point is accepted.
func (c *VerificationCase) advance(points []Checkpoint, at time.Time) error {
	if c.State != CaseAwaitingMeasurement {
		return fmt.Errorf("%w: case is not awaiting measurement", ErrStateConflict)
	}
	switch {
	case hasStatus(points, CheckpointRejected):
		c.State = CaseRecheckRequired
	case AllAccepted(points):
		c.State = CaseReviewReady
	default:
		c.State = CaseAwaitingMeasurement
	}
	c.Version++
	c.UpdatedAt = at.UTC()
	return nil
}

// RecordCheckpoint records one point result and moves the case according to the
// aggregate plan state in the same decision. The case state is checked before
// touching the point, so a sealed or blocked case reports a state conflict
// rather than a duplicate point.
func (c *VerificationCase) RecordCheckpoint(points []Checkpoint, point *Checkpoint, measurementID, technician string, accepted bool, at time.Time) error {
	if c.State != CaseAwaitingMeasurement {
		return fmt.Errorf("%w: case is not awaiting measurement", ErrStateConflict)
	}
	if err := point.Record(measurementID, technician, accepted, at); err != nil {
		return err
	}
	return c.advance(points, at)
}

// Reopen resets every rejected point for supplementary measurement while
// accepted points keep their basis. Only a case blocked by a rejection can be
// reopened; it returns the labels of the reset points.
func (c *VerificationCase) Reopen(points []Checkpoint, at time.Time) ([]string, error) {
	if c.State != CaseRecheckRequired {
		return nil, fmt.Errorf("%w: only a recheck_required case can be reopened", ErrStateConflict)
	}
	var reset []string
	for index := range points {
		if points[index].Status == CheckpointRejected {
			if err := points[index].Reset(); err != nil {
				return nil, err
			}
			reset = append(reset, points[index].Label)
		}
	}
	if len(reset) == 0 {
		return nil, fmt.Errorf("%w: case has no rejected checkpoint to reopen", ErrStateConflict)
	}
	c.State = CaseAwaitingMeasurement
	c.Version++
	c.UpdatedAt = at.UTC()
	return reset, nil
}

// Seal closes the case only when every checkpoint is accepted, so a failed or
// unrecorded point can never be sealed as a complete evidence package.
func (c *VerificationCase) Seal(points []Checkpoint, at time.Time) error {
	if c.State == CaseSealed {
		return fmt.Errorf("%w: sealed case cannot be sealed again", ErrStateConflict)
	}
	if c.State != CaseReviewReady {
		return fmt.Errorf("%w: case is not ready for review", ErrStateConflict)
	}
	if !AllAccepted(points) {
		return fmt.Errorf("%w: every checkpoint must be accepted before sealing", ErrCheckpointsOpen)
	}
	stamp := at.UTC()
	c.State = CaseSealed
	c.Version++
	c.UpdatedAt = stamp
	c.SealedAt = &stamp
	return nil
}

func hasStatus(points []Checkpoint, status CheckpointStatus) bool {
	for _, point := range points {
		if point.Status == status {
			return true
		}
	}
	return false
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
