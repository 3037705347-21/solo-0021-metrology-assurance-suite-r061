package assurance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

type Service struct {
	repository *repository.Repository
	now        func() time.Time
}

func NewService(repo *repository.Repository) *Service {
	return &Service{
		repository: repo,
		now:        time.Now,
	}
}

func (s *Service) RegisterDevice(ctx context.Context, input contracts.RegisterDeviceRequest) (domain.Device, error) {
	var created domain.Device
	tag := domain.NormalizeAssetTag(input.AssetTag)
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		if _, exists := m.DeviceByTag(tag); exists {
			return fmt.Errorf("%w: %s", domain.ErrDuplicateAssetTag, tag)
		}
		item, err := domain.NewDevice(
			m.NextID("device"),
			input.AssetTag,
			input.Model,
			input.Room,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutDevice(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"device",
			item.ID,
			"",
			"device_enrolled",
			item.Steward,
			"device enrolled for assurance work",
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		created = item
		return nil
	})
	return created, err
}

func (s *Service) Device(ctx context.Context, id string) (domain.Device, error) {
	var found domain.Device
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Device(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, id)
		}
		found = item
		return nil
	})
	return found, err
}

func (s *Service) OpenCase(ctx context.Context, input contracts.OpenCaseRequest) (contracts.CaseResponse, error) {
	plans, err := checkpointPlans(input)
	if err != nil {
		return contracts.CaseResponse{}, err
	}
	var result contracts.CaseResponse
	err = s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		deviceID := strings.TrimSpace(input.DeviceID)
		device, exists := m.Device(deviceID)
		if !exists {
			return fmt.Errorf("%w: device %s", domain.ErrNotFound, deviceID)
		}
		if !device.IsActive() {
			return fmt.Errorf("%w: device %s is %s", domain.ErrStateConflict, device.ID, device.Status)
		}
		if active, exists := m.ActiveCaseForDevice(device.ID); exists {
			return fmt.Errorf("%w: case %s is still %s", domain.ErrActiveCaseExists, active.ID, active.State)
		}
		item, err := domain.NewVerificationCase(
			m.NextID("case"),
			device.ID,
			plans,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_opened",
			item.Steward,
			fmt.Sprintf("verification case opened with %d checkpoint(s)", len(item.Checkpoints)),
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

// checkpointPlans resolves an open request into a fixed point plan. An
// explicit checkpoints array is the multi-point contract; otherwise the
// legacy nominal/tolerance pair is synthesized into one default point.
func checkpointPlans(input contracts.OpenCaseRequest) ([]domain.CheckpointPlan, error) {
	if input.Checkpoints == nil {
		return []domain.CheckpointPlan{{
			Code:      domain.DefaultCheckpointCode,
			Label:     domain.DefaultCheckpointCode,
			Nominal:   input.Nominal,
			Tolerance: input.Tolerance,
		}}, nil
	}
	plans := *input.Checkpoints
	if len(plans) == 0 {
		return nil, fmt.Errorf("%w: checkpoints must contain at least one point", domain.ErrInvalidInput)
	}
	result := make([]domain.CheckpointPlan, 0, len(plans))
	for _, plan := range plans {
		result = append(result, domain.CheckpointPlan{
			Code:      plan.Code,
			Label:     plan.Label,
			Nominal:   plan.Nominal,
			Tolerance: plan.Tolerance,
		})
	}
	return result, nil
}

func (s *Service) Case(ctx context.Context, id string) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Read(ctx, func(snapshot repository.Snapshot) error {
		item, exists := snapshot.Case(strings.TrimSpace(id))
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		result = caseResponseFromSnapshot(snapshot, item)
		return nil
	})
	return result, err
}

func (s *Service) RecordMeasurement(ctx context.Context, caseID string, input contracts.RecordMeasurementRequest) (contracts.CaseResponse, error) {
	return s.recordAtPoint(ctx, caseID, domain.DefaultCheckpointCode, input.Observed, input.Technician)
}

func (s *Service) RecordCheckpoint(ctx context.Context, caseID string, input contracts.RecordCheckpointRequest) (contracts.CaseResponse, error) {
	code := strings.TrimSpace(input.Code)
	if code == "" {
		return contracts.CaseResponse{}, fmt.Errorf("%w: checkpoint code is required", domain.ErrInvalidInput)
	}
	return s.recordAtPoint(ctx, caseID, code, input.Observed, input.Technician)
}

func (s *Service) recordAtPoint(ctx context.Context, caseID, pointCode string, observed float64, technician string) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		point, exists := item.Checkpoint(pointCode)
		if !exists {
			return fmt.Errorf("%w: checkpoint %s", domain.ErrCheckpointMissing, pointCode)
		}
		measurement, err := domain.NewMeasurement(
			m.NextID("measurement"),
			item.ID,
			point.ID,
			observed,
			point.Nominal,
			point.Tolerance,
			technician,
			s.now(),
		)
		if err != nil {
			return err
		}
		recorded, err := item.RecordCheckpoint(pointCode, measurement, s.now())
		if err != nil {
			return err
		}
		if err := m.AppendMeasurement(measurement); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		kind := "checkpoint_failed"
		detail := fmt.Sprintf("checkpoint %s (%s) measured %v against nominal %v with tolerance %v: out of tolerance",
			recorded.Code, recorded.Label, measurement.Observed, recorded.Nominal, recorded.Tolerance)
		if measurement.Accepted {
			kind = "checkpoint_passed"
			detail = fmt.Sprintf("checkpoint %s (%s) measured %v against nominal %v with tolerance %v: within tolerance",
				recorded.Code, recorded.Label, measurement.Observed, recorded.Nominal, recorded.Tolerance)
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			kind,
			measurement.Technician,
			detail,
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) ReopenCase(ctx context.Context, caseID string, input contracts.ReopenCaseRequest) (contracts.CaseResponse, error) {
	actor := strings.TrimSpace(input.Actor)
	reason := strings.TrimSpace(input.Reason)
	if actor == "" || reason == "" {
		return contracts.CaseResponse{}, fmt.Errorf("%w: actor and reason are required", domain.ErrInvalidInput)
	}
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		reset, err := item.Reopen(s.now())
		if err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_reopened",
			actor,
			fmt.Sprintf("%s; %d failed checkpoint(s) reset for remeasurement", reason, reset),
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func (s *Service) SealCase(ctx context.Context, caseID string, input contracts.SealCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		if _, exists := m.Seal(item.ID); exists {
			return fmt.Errorf("%w: case %s", domain.ErrSealAlreadyCreated, item.ID)
		}
		if !item.AllPassed() {
			total, pending, _, failed := item.DecisionCounts()
			return fmt.Errorf("%w: case cannot be sealed with %d/%d points closed (%d pending, %d failed)",
				domain.ErrStateConflict, total-pending-failed, total, pending, failed)
		}
		measurement, ok := bestAcceptedMeasurement(m.Measurements(item.ID))
		if !ok {
			return fmt.Errorf("%w: an accepted measurement is required before sealing", domain.ErrStateConflict)
		}
		sealedPoints := sealedCheckpoints(item)
		if err := item.Seal(s.now()); err != nil {
			return err
		}
		seal, err := domain.NewEvidenceSeal(
			m.NextID("seal"),
			item.ID,
			item.DeviceID,
			measurement.ID,
			input.Reviewer,
			input.Summary,
			sealedPoints,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutSeal(seal); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_sealed",
			seal.Reviewer,
			fmt.Sprintf("%s; %d checkpoint(s) frozen", seal.Summary, len(sealedPoints)),
			s.now(),
		)
		if err != nil {
			return err
		}
		m.AppendEvent(event)
		result = caseResponse(m, item)
		return nil
	})
	return result, err
}

func sealedCheckpoints(item domain.VerificationCase) []domain.SealedCheckpoint {
	points := make([]domain.SealedCheckpoint, 0, len(item.Checkpoints))
	for _, point := range item.Checkpoints {
		if point.Status != domain.CheckpointPassed || point.RecordedAt == nil {
			continue
		}
		points = append(points, domain.SealedCheckpoint{
			Code:          point.Code,
			Label:         point.Label,
			Nominal:       point.Nominal,
			Tolerance:     point.Tolerance,
			Observed:      point.Observed,
			Deviation:     point.Deviation,
			Attempts:      point.Attempts,
			MeasurementID: point.MeasurementID,
			Technician:    point.Technician,
			RecordedAt:    *point.RecordedAt,
		})
	}
	return points
}

func bestAcceptedMeasurement(items []domain.Measurement) (domain.Measurement, bool) {
	var selected domain.Measurement
	found := false
	for _, item := range items {
		if !item.Accepted {
			continue
		}
		if !found || item.AbsoluteDeviation() < selected.AbsoluteDeviation() {
			selected = item
			found = true
		}
	}
	return selected, found
}

func caseResponse(m *repository.Mutation, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: m.Measurements(item.ID),
		Events:       m.Events(item.ID),
	}
	if seal, exists := m.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}

func caseResponseFromSnapshot(snapshot repository.Snapshot, item domain.VerificationCase) contracts.CaseResponse {
	result := contracts.CaseResponse{
		Case:         item,
		Measurements: snapshot.Measurements(item.ID),
		Events:       snapshot.Events(item.ID),
	}
	if seal, exists := snapshot.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}
