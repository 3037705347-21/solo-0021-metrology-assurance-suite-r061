package domain

import (
	"fmt"
	"strings"
	"time"
)

// SealedCheckpoint freezes the final decision and evidence reference of one
// planned point inside the evidence seal.
type SealedCheckpoint struct {
	Code          string    `json:"code"`
	Label         string    `json:"label"`
	Nominal       float64   `json:"nominal"`
	Tolerance     float64   `json:"tolerance"`
	Observed      float64   `json:"observed"`
	Deviation     float64   `json:"deviation"`
	Attempts      int       `json:"attempts"`
	MeasurementID string    `json:"measurement_id"`
	Technician    string    `json:"technician"`
	RecordedAt    time.Time `json:"recorded_at"`
}

type EvidenceSeal struct {
	ID              string             `json:"id"`
	CaseID          string             `json:"case_id"`
	DeviceID        string             `json:"device_id"`
	MeasurementID   string             `json:"measurement_id"`
	Reviewer        string             `json:"reviewer"`
	Summary         string             `json:"summary"`
	SealedAt        time.Time          `json:"sealed_at"`
	Checkpoints     []SealedCheckpoint `json:"checkpoints"`
	CheckpointCount int                `json:"checkpoint_count"`
}

func NewEvidenceSeal(id, caseID, deviceID, measurementID, reviewer, summary string, points []SealedCheckpoint, at time.Time) (EvidenceSeal, error) {
	item := EvidenceSeal{
		ID:              strings.TrimSpace(id),
		CaseID:          strings.TrimSpace(caseID),
		DeviceID:        strings.TrimSpace(deviceID),
		MeasurementID:   strings.TrimSpace(measurementID),
		Reviewer:        strings.TrimSpace(reviewer),
		Summary:         strings.TrimSpace(summary),
		SealedAt:        at.UTC(),
		Checkpoints:     append([]SealedCheckpoint(nil), points...),
		CheckpointCount: len(points),
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
	if s.SealedAt.IsZero() {
		return fmt.Errorf("%w: seal time is required", ErrInvalidInput)
	}
	if s.CheckpointCount == 0 || len(s.Checkpoints) != s.CheckpointCount {
		return fmt.Errorf("%w: seal must freeze every planned checkpoint", ErrInvalidInput)
	}
	seenCodes := make(map[string]struct{}, s.CheckpointCount)
	for _, point := range s.Checkpoints {
		if point.Code == "" || point.Label == "" || point.MeasurementID == "" || point.Technician == "" || point.RecordedAt.IsZero() {
			return fmt.Errorf("%w: each sealed checkpoint needs a decision and evidence reference", ErrInvalidInput)
		}
		if !isFinite(point.Nominal) || !isFinite(point.Tolerance) || point.Tolerance <= 0 {
			return fmt.Errorf("%w: sealed checkpoint %q has invalid limits", ErrInvalidInput, point.Code)
		}
		if !isFinite(point.Observed) || !isFinite(point.Deviation) {
			return fmt.Errorf("%w: sealed checkpoint %q has an invalid result", ErrInvalidInput, point.Code)
		}
		if abs(point.Observed-point.Nominal) > point.Tolerance {
			return fmt.Errorf("%w: sealed checkpoint %q is out of tolerance", ErrInvalidInput, point.Code)
		}
		if _, exists := seenCodes[point.Code]; exists {
			return fmt.Errorf("%w: sealed checkpoint %q is repeated", ErrInvalidInput, point.Code)
		}
		seenCodes[point.Code] = struct{}{}
	}
	return nil
}
