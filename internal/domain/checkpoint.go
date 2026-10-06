package domain

import (
	"fmt"
	"strings"
	"time"
)

type CheckpointStatus string

const (
	CheckpointPending  CheckpointStatus = "pending"
	CheckpointAccepted CheckpointStatus = "accepted"
	CheckpointRejected CheckpointStatus = "rejected"
)

const MaxCheckpointsPerCase = 50

// Checkpoint is one required measurement point defined when a case is opened.
// Its current basis is the measurement that most recently closed the point;
// every attempt also remains in the case measurement history for traceability.
type Checkpoint struct {
	ID            string           `json:"id"`
	CaseID        string           `json:"case_id"`
	Ordinal       int              `json:"ordinal"`
	Label         string           `json:"label"`
	Nominal       float64          `json:"nominal"`
	Tolerance     float64          `json:"tolerance"`
	Status        CheckpointStatus `json:"status"`
	MeasurementID string           `json:"measurement_id,omitempty"`
	Technician    string           `json:"technician,omitempty"`
	RecordedAt    *time.Time       `json:"recorded_at,omitempty"`
}

func NewCheckpoint(id, caseID, label string, ordinal int, nominal, tolerance float64) (Checkpoint, error) {
	item := Checkpoint{
		ID:        strings.TrimSpace(id),
		CaseID:    strings.TrimSpace(caseID),
		Ordinal:   ordinal,
		Label:     strings.TrimSpace(label),
		Nominal:   nominal,
		Tolerance: tolerance,
		Status:    CheckpointPending,
	}
	if err := item.Validate(); err != nil {
		return Checkpoint{}, err
	}
	return item, nil
}

func (p Checkpoint) Validate() error {
	if p.ID == "" || p.CaseID == "" || p.Label == "" {
		return fmt.Errorf("%w: checkpoint id, case id, and label are required", ErrInvalidInput)
	}
	if len(p.Label) > 64 {
		return fmt.Errorf("%w: checkpoint label is too long", ErrInvalidInput)
	}
	if p.Ordinal < 1 {
		return fmt.Errorf("%w: checkpoint ordinal must start at one", ErrInvalidInput)
	}
	if !isFinite(p.Nominal) || !isFinite(p.Tolerance) || p.Tolerance <= 0 {
		return fmt.Errorf("%w: checkpoint nominal must be finite and tolerance must be positive", ErrInvalidInput)
	}
	switch p.Status {
	case CheckpointPending, CheckpointAccepted, CheckpointRejected:
	default:
		return fmt.Errorf("%w: unsupported checkpoint state %q", ErrInvalidInput, p.Status)
	}
	switch p.Status {
	case CheckpointPending:
		if p.MeasurementID != "" || p.Technician != "" || p.RecordedAt != nil {
			return fmt.Errorf("%w: pending checkpoint cannot carry a measurement basis", ErrInvalidInput)
		}
	default:
		if p.MeasurementID == "" || p.Technician == "" || p.RecordedAt == nil || p.RecordedAt.IsZero() {
			return fmt.Errorf("%w: closed checkpoint requires its measurement basis", ErrInvalidInput)
		}
	}
	return nil
}

// Record closes a pending point with the measurement that supports the decision.
func (p *Checkpoint) Record(measurementID, technician string, accepted bool, at time.Time) error {
	if p.Status != CheckpointPending {
		return fmt.Errorf("%w: checkpoint %s is already %s", ErrCheckpointComplete, p.Label, p.Status)
	}
	stamp := at.UTC()
	p.MeasurementID = strings.TrimSpace(measurementID)
	p.Technician = strings.TrimSpace(technician)
	p.RecordedAt = &stamp
	if accepted {
		p.Status = CheckpointAccepted
	} else {
		p.Status = CheckpointRejected
	}
	return p.Validate()
}

// Reset returns a rejected point to pending so a supplementary measurement can
// be recorded. The rejected measurement itself stays in case history.
func (p *Checkpoint) Reset() error {
	if p.Status != CheckpointRejected {
		return fmt.Errorf("%w: only a rejected checkpoint can be reset", ErrStateConflict)
	}
	p.Status = CheckpointPending
	p.MeasurementID = ""
	p.Technician = ""
	p.RecordedAt = nil
	return nil
}

func (p Checkpoint) IsClosed() bool {
	return p.Status != CheckpointPending
}

// PlanCounts summarizes a checkpoint plan in its stored ordinal order.
func PlanCounts(points []Checkpoint) (pending, accepted, rejected int) {
	for _, point := range points {
		switch point.Status {
		case CheckpointPending:
			pending++
		case CheckpointAccepted:
			accepted++
		case CheckpointRejected:
			rejected++
		}
	}
	return pending, accepted, rejected
}

// AllAccepted reports whether every plan point is closed with an accepted basis.
func AllAccepted(points []Checkpoint) bool {
	if len(points) == 0 {
		return false
	}
	for _, point := range points {
		if point.Status != CheckpointAccepted {
			return false
		}
	}
	return true
}
