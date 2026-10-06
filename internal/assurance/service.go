package assurance

import (
	"context"
	"fmt"
	"math"
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
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
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

		caseID := m.NextID("case")
		plan, nominal, tolerance, err := buildPlan(m, caseID, input)
		if err != nil {
			return err
		}
		item, err := domain.NewVerificationCase(
			caseID,
			device.ID,
			nominal,
			tolerance,
			input.Steward,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := m.PutCase(item); err != nil {
			return err
		}
		if err := m.PutCheckpoints(plan); err != nil {
			return err
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			"case_opened",
			item.Steward,
			planSummary(plan),
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

// buildPlan turns the request into the ordered checkpoint plan. A request with
// an explicit plan defines one point per entry; a legacy single nominal and
// tolerance creates one default point. The two forms cannot be mixed.
func buildPlan(m *repository.Mutation, caseID string, input contracts.OpenCaseRequest) ([]domain.Checkpoint, float64, float64, error) {
	hasPlan := len(input.Checkpoints) > 0
	if hasPlan {
		if input.Nominal != 0 || input.Tolerance != 0 {
			return nil, 0, 0, fmt.Errorf("%w: case-level nominal and tolerance cannot be combined with checkpoints", domain.ErrInvalidInput)
		}
		if len(input.Checkpoints) > domain.MaxCheckpointsPerCase {
			return nil, 0, 0, fmt.Errorf("%w: a case plan can contain at most %d checkpoints", domain.ErrInvalidInput, domain.MaxCheckpointsPerCase)
		}
		plan := make([]domain.Checkpoint, 0, len(input.Checkpoints))
		labels := make(map[string]bool, len(input.Checkpoints))
		for index, entry := range input.Checkpoints {
			label := strings.TrimSpace(entry.Label)
			if labels[label] {
				return nil, 0, 0, fmt.Errorf("%w: checkpoint label %q is repeated", domain.ErrDuplicateCheckpoint, label)
			}
			labels[label] = true
			point, err := domain.NewCheckpoint(
				m.NextID("checkpoint"),
				caseID,
				entry.Label,
				index+1,
				entry.Nominal,
				entry.Tolerance,
			)
			if err != nil {
				return nil, 0, 0, err
			}
			plan = append(plan, point)
		}
		return plan, 0, 0, nil
	}
	if math.IsNaN(input.Nominal) || math.IsInf(input.Nominal, 0) || math.IsNaN(input.Tolerance) || math.IsInf(input.Tolerance, 0) || input.Tolerance <= 0 {
		return nil, 0, 0, fmt.Errorf("%w: nominal must be finite and tolerance must be positive", domain.ErrInvalidInput)
	}
	point, err := domain.NewCheckpoint(m.NextID("checkpoint"), caseID, "default", 1, input.Nominal, input.Tolerance)
	if err != nil {
		return nil, 0, 0, err
	}
	return []domain.Checkpoint{point}, input.Nominal, input.Tolerance, nil
}

func planSummary(plan []domain.Checkpoint) string {
	if len(plan) == 1 && plan[0].Label == "default" {
		return "verification case opened with one default checkpoint"
	}
	return fmt.Sprintf("verification case opened with %d required checkpoints", len(plan))
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
	var result contracts.CaseResponse
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		points := m.Checkpoints(item.ID)
		point, err := resolveCheckpoint(points, input.CheckpointID)
		if err != nil {
			return err
		}
		measurement, err := domain.NewMeasurement(
			m.NextID("measurement"),
			item.ID,
			point.ID,
			point.Label,
			input.Observed,
			point.Nominal,
			point.Tolerance,
			input.Technician,
			s.now(),
		)
		if err != nil {
			return err
		}
		index := checkpointIndex(points, point.ID)
		if err := item.RecordCheckpoint(points, &points[index], measurement.ID, measurement.Technician, measurement.Accepted, s.now()); err != nil {
			return err
		}
		if err := m.AppendMeasurement(measurement); err != nil {
			return err
		}
		if err := m.ReplaceCheckpoints(points); err != nil {
			return err
		}
		if err := m.UpdateCase(item); err != nil {
			return err
		}
		kind := "measurement_rejected"
		verdict := "rejected; case requires recheck before more points can close"
		if measurement.Accepted {
			kind = "measurement_accepted"
			verdict = "accepted"
		}
		event, err := domain.NewAuditEvent(
			m.NextID("event"),
			"case",
			item.ID,
			item.ID,
			kind,
			measurement.Technician,
			fmt.Sprintf("checkpoint %d %q: observed %g vs nominal %g ± %g, deviation %g, %s",
				point.Ordinal, point.Label, measurement.Observed, point.Nominal, point.Tolerance, measurement.Deviation, verdict),
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

// resolveCheckpoint maps a request to one plan point. A missing checkpoint id
// only works for a single-point case; an unknown id is never invented.
func resolveCheckpoint(points []domain.Checkpoint, requested string) (domain.Checkpoint, error) {
	requested = strings.TrimSpace(requested)
	if requested != "" {
		for _, point := range points {
			if point.ID == requested {
				return point, nil
			}
		}
		return domain.Checkpoint{}, fmt.Errorf("%w: checkpoint %s", domain.ErrNotFound, requested)
	}
	if len(points) == 1 {
		return points[0], nil
	}
	return domain.Checkpoint{}, fmt.Errorf("%w: checkpoint_id is required for a %d-point case", domain.ErrInvalidInput, len(points))
}

func checkpointIndex(points []domain.Checkpoint, checkpointID string) int {
	for index := range points {
		if points[index].ID == checkpointID {
			return index
		}
	}
	return -1
}

func (s *Service) ReopenCase(ctx context.Context, caseID string, input contracts.ReopenCaseRequest) (contracts.CaseResponse, error) {
	var result contracts.CaseResponse
	actor := strings.TrimSpace(input.Actor)
	reason := strings.TrimSpace(input.Reason)
	if actor == "" || reason == "" {
		return result, fmt.Errorf("%w: actor and reason are required", domain.ErrInvalidInput)
	}
	err := s.repository.Mutate(ctx, func(m *repository.Mutation) error {
		id := strings.TrimSpace(caseID)
		item, exists := m.Case(id)
		if !exists {
			return fmt.Errorf("%w: case %s", domain.ErrNotFound, id)
		}
		points := m.Checkpoints(item.ID)
		reset, err := item.Reopen(points, s.now())
		if err != nil {
			return err
		}
		if err := m.ReplaceCheckpoints(points); err != nil {
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
			fmt.Sprintf("%s (reset rejected checkpoints: %s)", reason, strings.Join(reset, ", ")),
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
		points := m.Checkpoints(item.ID)
		if len(points) == 0 {
			return fmt.Errorf("%w: case %s has no measurement plan", domain.ErrStateConflict, item.ID)
		}
		// Gate first: only a review_ready case whose every point is accepted
		// may proceed. If anything below fails the whole commit is discarded.
		if err := item.Seal(points, s.now()); err != nil {
			return err
		}

		// Freeze one basis per point into the immutable evidence package.
		measurements := m.Measurements(item.ID)
		byID := make(map[string]domain.Measurement, len(measurements))
		for _, measurement := range measurements {
			byID[measurement.ID] = measurement
		}
		sealedPoints := make([]domain.SealedPoint, 0, len(points))
		for _, point := range points {
			measurement, ok := byID[point.MeasurementID]
			if !ok {
				return fmt.Errorf("%w: checkpoint %q is missing its measurement basis", domain.ErrCheckpointsOpen, point.Label)
			}
			sealedPoint, err := domain.NewSealedPoint(point, measurement)
			if err != nil {
				return err
			}
			sealedPoints = append(sealedPoints, sealedPoint)
		}
		measurement, ok := bestAcceptedMeasurement(measurements)
		if !ok {
			return fmt.Errorf("%w: an accepted measurement is required before sealing", domain.ErrStateConflict)
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
			fmt.Sprintf("%s (%d checkpoints closed as one evidence package)", seal.Summary, len(sealedPoints)),
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
		Checkpoints:  m.Checkpoints(item.ID),
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
		Checkpoints:  snapshot.Checkpoints(item.ID),
		Measurements: snapshot.Measurements(item.ID),
		Events:       snapshot.Events(item.ID),
	}
	if seal, exists := snapshot.Seal(item.ID); exists {
		result.Seal = &seal
	}
	return result
}
