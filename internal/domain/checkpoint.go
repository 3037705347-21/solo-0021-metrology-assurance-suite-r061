package domain

import (
	"fmt"
	"strings"
	"time"
)

type CheckpointStatus string

const (
	CheckpointPending CheckpointStatus = "pending"
	CheckpointPassed  CheckpointStatus = "passed"
	CheckpointFailed  CheckpointStatus = "failed"

	// DefaultCheckpointCode identifies the single point synthesized for cases
	// opened through the legacy nominal/tolerance contract.
	DefaultCheckpointCode = "default"

	maxCheckpointLabelLength = 128
	maxCheckpointCodeLength  = 64
)

// CheckpointPlan is one point of the measurement plan fixed when the steward
// opens a case. Each point has its own nominal value and tolerance.
type CheckpointPlan struct {
	Code      string  `json:"code"`
	Label     string  `json:"label"`
	Nominal   float64 `json:"nominal"`
	Tolerance float64 `json:"tolerance"`
}

// Checkpoint is the case-owned, mutable state of one planned measurement
// point. The point holds only its latest decision; every attempt remains
// traceable as a Measurement with a matching CheckpointID.
type Checkpoint struct {
	ID            string           `json:"id"`
	Order         int              `json:"order"`
	Code          string           `json:"code"`
	Label         string           `json:"label"`
	Nominal       float64          `json:"nominal"`
	Tolerance     float64          `json:"tolerance"`
	Status        CheckpointStatus `json:"status"`
	Attempts      int              `json:"attempts"`
	MeasurementID string           `json:"measurement_id,omitempty"`
	Observed      float64          `json:"observed"`
	Deviation     float64          `json:"deviation"`
	Technician    string           `json:"technician,omitempty"`
	RecordedAt    *time.Time       `json:"recorded_at,omitempty"`
}

// BuildCheckpoints turns a validated request into the ordered points of a new
// measurement plan. Codes and labels are normalized and defaulted, and the
// plan is rejected when identities collide or a point is malformed.
func BuildCheckpoints(plans []CheckpointPlan) ([]Checkpoint, error) {
	if len(plans) == 0 {
		return nil, fmt.Errorf("%w: at least one checkpoint is required", ErrInvalidInput)
	}
	points := make([]Checkpoint, 0, len(plans))
	codes := make(map[string]int, len(plans))
	labels := make(map[string]int, len(plans))
	for index, plan := range plans {
		code := strings.TrimSpace(plan.Code)
		if code == "" {
			code = fmt.Sprintf("cp-%02d", index+1)
		}
		label := strings.TrimSpace(plan.Label)
		if label == "" {
			label = code
		}
		if len(code) > maxCheckpointCodeLength || len(label) > maxCheckpointLabelLength {
			return nil, fmt.Errorf("%w: checkpoint %q fields are too long", ErrInvalidInput, code)
		}
		if !isFinite(plan.Nominal) || !isFinite(plan.Tolerance) || plan.Tolerance <= 0 {
			return nil, fmt.Errorf("%w: checkpoint %q needs a finite nominal and a positive tolerance", ErrInvalidInput, code)
		}
		if prior, exists := codes[code]; exists {
			return nil, fmt.Errorf("%w: checkpoint code %q is repeated at positions %d and %d", ErrInvalidInput, code, prior+1, index+1)
		}
		if prior, exists := labels[label]; exists {
			return nil, fmt.Errorf("%w: checkpoint label %q is repeated at positions %d and %d", ErrInvalidInput, label, prior+1, index+1)
		}
		codes[code] = index
		labels[label] = index
		points = append(points, Checkpoint{
			ID:        fmt.Sprintf("%s-%03d", "chk", index+1),
			Order:     index + 1,
			Code:      code,
			Label:     label,
			Nominal:   plan.Nominal,
			Tolerance: plan.Tolerance,
			Status:    CheckpointPending,
		})
	}
	return points, nil
}

func (c Checkpoint) Validate() error {
	if c.ID == "" || c.Code == "" || c.Label == "" || c.Order < 1 {
		return fmt.Errorf("%w: checkpoint id, code, label, and order are required", ErrInvalidInput)
	}
	if len(c.Code) > maxCheckpointCodeLength || len(c.Label) > maxCheckpointLabelLength {
		return fmt.Errorf("%w: checkpoint %q fields are too long", ErrInvalidInput, c.Code)
	}
	if !isFinite(c.Nominal) || !isFinite(c.Tolerance) || c.Tolerance <= 0 {
		return fmt.Errorf("%w: checkpoint %q needs a finite nominal and a positive tolerance", ErrInvalidInput, c.Code)
	}
	switch c.Status {
	case CheckpointPending, CheckpointPassed, CheckpointFailed:
	default:
		return fmt.Errorf("%w: unsupported checkpoint status %q", ErrInvalidInput, c.Status)
	}
	if c.Attempts < 0 {
		return fmt.Errorf("%w: checkpoint attempt count cannot be negative", ErrInvalidInput)
	}
	if c.Status == CheckpointPending {
		if c.MeasurementID != "" || c.Technician != "" || c.RecordedAt != nil {
			return fmt.Errorf("%w: pending checkpoint %q cannot carry a decision", ErrInvalidInput, c.Code)
		}
		return nil
	}
	if c.MeasurementID == "" || c.Technician == "" || c.RecordedAt == nil || c.Attempts < 1 {
		return fmt.Errorf("%w: decided checkpoint %q must cite its measurement and technician", ErrInvalidInput, c.Code)
	}
	return nil
}

func (c Checkpoint) IsPending() bool { return c.Status == CheckpointPending }

// record stores the decision for one measurement attempt. Pending points only
// accept a single attempt; after a failed point is reset by reopen it is
// pending again and may record its next attempt.
func (c *Checkpoint) record(measurement Measurement, at time.Time) {
	stamp := at.UTC()
	c.Attempts++
	c.MeasurementID = measurement.ID
	c.Observed = measurement.Observed
	c.Deviation = measurement.Deviation
	c.Technician = measurement.Technician
	c.RecordedAt = &stamp
	if measurement.Accepted {
		c.Status = CheckpointPassed
	} else {
		c.Status = CheckpointFailed
	}
}

// resetForRetry clears only the latest decision of a failed point so it can be
// measured again after reopen. The historical attempts and measurements stay
// in the case record.
func (c *Checkpoint) resetForRetry() {
	c.Status = CheckpointPending
	c.MeasurementID = ""
	c.Observed = 0
	c.Deviation = 0
	c.Technician = ""
	c.RecordedAt = nil
}
