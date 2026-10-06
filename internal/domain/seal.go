package domain

import (
	"fmt"
	"strings"
	"time"
)

// SealedPoint is the frozen checkpoint basis embedded in the evidence seal.
// It preserves which measurement closed each point even if later events happen
// around the same device; the sealed record itself never changes.
type SealedPoint struct {
	Ordinal       int       `json:"ordinal"`
	CheckpointID  string    `json:"checkpoint_id"`
	Label         string    `json:"label"`
	Nominal       float64   `json:"nominal"`
	Tolerance     float64   `json:"tolerance"`
	Observed      float64   `json:"observed"`
	Deviation     float64   `json:"deviation"`
	MeasurementID string    `json:"measurement_id"`
	Technician    string    `json:"technician"`
	RecordedAt    time.Time `json:"recorded_at"`
}

func NewSealedPoint(point Checkpoint, measurement Measurement) (SealedPoint, error) {
	if point.Status != CheckpointAccepted {
		return SealedPoint{}, fmt.Errorf("%w: checkpoint %s is not accepted", ErrCheckpointsOpen, point.Label)
	}
	if measurement.ID != point.MeasurementID || !measurement.Accepted {
		return SealedPoint{}, fmt.Errorf("%w: checkpoint %s basis does not match an accepted measurement", ErrCheckpointsOpen, point.Label)
	}
	item := SealedPoint{
		Ordinal:       point.Ordinal,
		CheckpointID:  point.ID,
		Label:         point.Label,
		Nominal:       point.Nominal,
		Tolerance:     point.Tolerance,
		Observed:      measurement.Observed,
		Deviation:     measurement.Deviation,
		MeasurementID: measurement.ID,
		Technician:    measurement.Technician,
		RecordedAt:    measurement.CreatedAt,
	}
	if err := item.Validate(); err != nil {
		return SealedPoint{}, err
	}
	return item, nil
}

func (p SealedPoint) Validate() error {
	if p.CheckpointID == "" || p.Label == "" || p.MeasurementID == "" || p.Technician == "" {
		return fmt.Errorf("%w: sealed point requires checkpoint and measurement basis", ErrInvalidInput)
	}
	if p.Ordinal < 1 {
		return fmt.Errorf("%w: sealed point ordinal must start at one", ErrInvalidInput)
	}
	if !isFinite(p.Nominal) || !isFinite(p.Tolerance) || p.Tolerance <= 0 {
		return fmt.Errorf("%w: sealed point tolerance must be positive", ErrInvalidInput)
	}
	if p.RecordedAt.IsZero() {
		return fmt.Errorf("%w: sealed point requires the basis measurement time", ErrInvalidInput)
	}
	return nil
}

type EvidenceSeal struct {
	ID            string        `json:"id"`
	CaseID        string        `json:"case_id"`
	DeviceID      string        `json:"device_id"`
	MeasurementID string        `json:"measurement_id"`
	Reviewer      string        `json:"reviewer"`
	Summary       string        `json:"summary"`
	Points        []SealedPoint `json:"points"`
	SealedAt      time.Time     `json:"sealed_at"`
}

func NewEvidenceSeal(id, caseID, deviceID, measurementID, reviewer, summary string, points []SealedPoint, at time.Time) (EvidenceSeal, error) {
	item := EvidenceSeal{
		ID:            strings.TrimSpace(id),
		CaseID:        strings.TrimSpace(caseID),
		DeviceID:      strings.TrimSpace(deviceID),
		MeasurementID: strings.TrimSpace(measurementID),
		Reviewer:      strings.TrimSpace(reviewer),
		Summary:       strings.TrimSpace(summary),
		Points:        append([]SealedPoint(nil), points...),
		SealedAt:      at.UTC(),
	}
	if err := item.Validate(); err != nil {
		return EvidenceSeal{}, err
	}
	return item, nil
}

func (s EvidenceSeal) Validate() error {
	if s.ID == "" || s.CaseID == "" || s.DeviceID == "" || s.MeasurementID == "" || s.Reviewer == "" || s.Summary == "" {
		return fmt.Errorf("%w: seal id, case, device, measurement, reviewer, and summary are required", ErrInvalidInput)
	}
	if len(s.Points) == 0 {
		return fmt.Errorf("%w: seal requires at least one accepted checkpoint basis", ErrInvalidInput)
	}
	seen := make(map[string]bool, len(s.Points))
	for _, point := range s.Points {
		if err := point.Validate(); err != nil {
			return err
		}
		if seen[point.CheckpointID] {
			return fmt.Errorf("%w: seal contains checkpoint %s twice", ErrInvalidInput, point.Label)
		}
		seen[point.CheckpointID] = true
	}
	if s.SealedAt.IsZero() {
		return fmt.Errorf("%w: seal time is required", ErrInvalidInput)
	}
	return nil
}
