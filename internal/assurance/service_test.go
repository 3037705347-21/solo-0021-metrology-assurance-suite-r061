package assurance_test

import (
	"context"
	"sync"
	"testing"

	"example.com/solo-0021-metrology-assurance-suite/internal/assurance"
	"example.com/solo-0021-metrology-assurance-suite/internal/contracts"
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
	"example.com/solo-0021-metrology-assurance-suite/internal/repository"
)

func newService() (*assurance.Service, context.Context) {
	return assurance.NewService(repository.New()), context.Background()
}

func registerDevice(t *testing.T, svc *assurance.Service, ctx context.Context, tag string) string {
	t.Helper()
	device, err := svc.RegisterDevice(ctx, contracts.RegisterDeviceRequest{
		AssetTag: tag,
		Model:    "Bench Meter",
		Room:     "Room A",
		Steward:  "Lin",
	})
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	return device.ID
}

func threePointPlan() []contracts.CheckpointPlanItem {
	return []contracts.CheckpointPlanItem{
		{Label: "low", Nominal: 10, Tolerance: 0.1},
		{Label: "mid", Nominal: 25, Tolerance: 0.2},
		{Label: "high", Nominal: 50, Tolerance: 0.5},
	}
}

func openThreePointCase(t *testing.T, svc *assurance.Service, ctx context.Context, tag string) contracts.CaseResponse {
	t.Helper()
	deviceID := registerDevice(t, svc, ctx, tag)
	view, err := svc.OpenCase(ctx, contracts.OpenCaseRequest{
		DeviceID:    deviceID,
		Steward:     "Qiao",
		Checkpoints: threePointPlan(),
	})
	if err != nil {
		t.Fatalf("open case: %v", err)
	}
	return view
}

func TestOpenCasePlanValidation(t *testing.T) {
	svc, ctx := newService()

	t.Run("rejects duplicate labels", func(t *testing.T) {
		deviceID := registerDevice(t, svc, ctx, "MET-DUP")
		_, err := svc.OpenCase(ctx, contracts.OpenCaseRequest{
			DeviceID: deviceID,
			Steward:  "Qiao",
			Checkpoints: []contracts.CheckpointPlanItem{
				{Label: "mid", Nominal: 25, Tolerance: 0.2},
				{Label: " mid ", Nominal: 50, Tolerance: 0.5},
			},
		})
		if err == nil {
			t.Fatal("duplicate labels were accepted")
		}
	})

	t.Run("rejects mixing case nominal with points", func(t *testing.T) {
		deviceID := registerDevice(t, svc, ctx, "MET-MIX")
		_, err := svc.OpenCase(ctx, contracts.OpenCaseRequest{
			DeviceID:    deviceID,
			Nominal:     25,
			Tolerance:   0.5,
			Steward:     "Qiao",
			Checkpoints: threePointPlan(),
		})
		if err == nil {
			t.Fatal("mixed nominal and plan was accepted")
		}
	})
}

func TestLegacySinglePointCaseStillWorks(t *testing.T) {
	svc, ctx := newService()
	deviceID := registerDevice(t, svc, ctx, "MET-LEGACY")
	view, err := svc.OpenCase(ctx, contracts.OpenCaseRequest{
		DeviceID:  deviceID,
		Nominal:   25,
		Tolerance: 0.5,
		Steward:   "Qiao",
	})
	if err != nil {
		t.Fatalf("open legacy case: %v", err)
	}
	if len(view.Checkpoints) != 1 || view.Checkpoints[0].Label != "default" {
		t.Fatalf("legacy case plan = %+v", view.Checkpoints)
	}
	// checkpoint_id is optional on a single-point case.
	recorded, err := svc.RecordMeasurement(ctx, view.Case.ID, contracts.RecordMeasurementRequest{
		Observed:   25.2,
		Technician: "Rui",
	})
	if err != nil {
		t.Fatalf("record legacy: %v", err)
	}
	if recorded.Case.State != domain.CaseReviewReady {
		t.Fatalf("legacy state = %s", recorded.Case.State)
	}
}

func TestPointByPointCompletionFlow(t *testing.T) {
	svc, ctx := newService()
	view := openThreePointCase(t, svc, ctx, "MET-FLOW")
	caseID := view.Case.ID
	ids := checkpointIDs(t, view.Checkpoints)

	record := func(key string, observed float64) contracts.CaseResponse {
		t.Helper()
		out, err := svc.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{
			CheckpointID: ids[key],
			Observed:     observed,
			Technician:   "Rui",
		})
		if err != nil {
			t.Fatalf("record %s: %v", key, err)
		}
		return out
	}

	first := record("low", 10.05)
	if first.Case.State != domain.CaseAwaitingMeasurement {
		t.Fatalf("after first point state = %s", first.Case.State)
	}
	basis := findCheckpoint(t, first.Checkpoints, ids["low"])
	if basis.MeasurementID == "" || basis.Technician != "Rui" {
		t.Fatalf("accepted point basis missing: %+v", basis)
	}

	// Duplicate record on the closed point cannot fabricate anything.
	if _, err := svc.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{
		CheckpointID: ids["low"],
		Observed:     10.01,
		Technician:   "Rui",
	}); err == nil {
		t.Fatal("duplicate point record was accepted")
	}

	// Unknown point id cannot record work.
	if _, err := svc.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{
		CheckpointID: "checkpoint-0000",
		Observed:     10,
		Technician:   "Rui",
	}); err == nil {
		t.Fatal("unknown checkpoint was accepted")
	}

	// Missing point id on a multi-point case is rejected.
	if _, err := svc.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{
		Observed:   10,
		Technician: "Rui",
	}); err == nil {
		t.Fatal("missing checkpoint id was accepted")
	}

	record("mid", 24.9)
	finished := record("high", 50.2)
	if finished.Case.State != domain.CaseReviewReady {
		t.Fatalf("after all points state = %s", finished.Case.State)
	}
	if len(finished.Measurements) != 3 {
		t.Fatalf("measurement history = %d, want 3", len(finished.Measurements))
	}
}

func TestFailedPointBlockingAndRetestPreservesHistory(t *testing.T) {
	svc, ctx := newService()
	view := openThreePointCase(t, svc, ctx, "MET-RETEST")
	caseID := view.Case.ID
	ids := checkpointIDs(t, view.Checkpoints)

	if _, err := svc.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{CheckpointID: ids["low"], Observed: 10.05, Technician: "Rui"}); err != nil {
		t.Fatal(err)
	}
	failed, err := svc.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{CheckpointID: ids["mid"], Observed: 25.9, Technician: "Rui"})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Case.State != domain.CaseRecheckRequired {
		t.Fatalf("state after rejection = %s", failed.Case.State)
	}
	// The pending high point cannot close while the case is blocked.
	if _, err := svc.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{CheckpointID: ids["high"], Observed: 50.1, Technician: "Rui"}); err == nil {
		t.Fatal("record advanced while a point was rejected")
	}
	// Seal is impossible while blocked.
	if _, err := svc.SealCase(ctx, caseID, contracts.SealCaseRequest{Reviewer: "V", Summary: "early"}); err == nil {
		t.Fatal("seal was accepted with a rejected point")
	}

	reopened, err := svc.ReopenCase(ctx, caseID, contracts.ReopenCaseRequest{
		Actor:  "Lin",
		Reason: "probe re-seated",
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Case.State != domain.CaseAwaitingMeasurement {
		t.Fatalf("reopened state = %s", reopened.Case.State)
	}
	after := checkpointMap(reopened.Checkpoints)
	if after[ids["low"]].Status != domain.CheckpointAccepted {
		t.Fatal("accepted point was disturbed by reopen")
	}
	if after[ids["mid"]].Status != domain.CheckpointPending {
		t.Fatalf("rejected point status = %s, want pending", after[ids["mid"]].Status)
	}
	if after[ids["mid"]].MeasurementID != "" {
		t.Fatal("rejected point basis not cleared for retest")
	}
	// Rejected attempt remains in immutable history.
	if len(reopened.Measurements) != 2 {
		t.Fatalf("history after reopen = %d, want 2", len(reopened.Measurements))
	}

	if _, err := svc.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{CheckpointID: ids["mid"], Observed: 25.05, Technician: "Rui"}); err != nil {
		t.Fatal(err)
	}
	finished, err := svc.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{CheckpointID: ids["high"], Observed: 50.2, Technician: "Rui"})
	if err != nil {
		t.Fatal(err)
	}
	if finished.Case.State != domain.CaseReviewReady {
		t.Fatalf("state after retest = %s", finished.Case.State)
	}
}

func TestSealFreezesEveryPointBasis(t *testing.T) {
	svc, ctx := newService()
	view := openThreePointCase(t, svc, ctx, "MET-SEAL")
	caseID := view.Case.ID
	ids := checkpointIDs(t, view.Checkpoints)
	observed := map[string]float64{"low": 10.03, "mid": 25.10, "high": 49.80}
	for _, key := range []string{"low", "mid", "high"} {
		if _, err := svc.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{CheckpointID: ids[key], Observed: observed[key], Technician: "Rui"}); err != nil {
			t.Fatal(err)
		}
	}
	sealed, err := svc.SealCase(ctx, caseID, contracts.SealCaseRequest{
		Reviewer: "Verifier",
		Summary:  "three point package",
	})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if sealed.Case.State != domain.CaseSealed || sealed.Seal == nil {
		t.Fatalf("seal response incomplete: %+v", sealed.Case)
	}
	if len(sealed.Seal.Points) != 3 {
		t.Fatalf("seal contains %d points, want 3", len(sealed.Seal.Points))
	}
	byLabel := make(map[string]domain.SealedPoint, len(sealed.Seal.Points))
	for _, point := range sealed.Seal.Points {
		byLabel[point.Label] = point
		if point.MeasurementID == "" || point.RecordedAt.IsZero() {
			t.Fatalf("sealed point %q missing basis", point.Label)
		}
	}
	if byLabel["mid"].Observed != 25.10 {
		t.Fatalf("frozen mid observed = %v", byLabel["mid"].Observed)
	}

	// Sealed cases reject every later mutation.
	if _, err := svc.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{CheckpointID: ids["low"], Observed: 10.09, Technician: "Rui"}); err == nil {
		t.Fatal("sealed case accepted another measurement")
	}
	if _, err := svc.ReopenCase(ctx, caseID, contracts.ReopenCaseRequest{Actor: "Lin", Reason: "x"}); err == nil {
		t.Fatal("sealed case was reopened")
	}
	if _, err := svc.SealCase(ctx, caseID, contracts.SealCaseRequest{Reviewer: "V2", Summary: "again"}); err == nil {
		t.Fatal("sealed case was sealed twice")
	}
}

func TestConcurrentLastPointRecordedExactlyOnce(t *testing.T) {
	svc, ctx := newService()
	view := openThreePointCase(t, svc, ctx, "MET-RACE-RECORD")
	caseID := view.Case.ID
	ids := checkpointIDs(t, view.Checkpoints)
	acceptedObserved := map[string]float64{"low": 10.05, "mid": 25.05, "high": 50.05}
	for _, key := range []string{"low", "mid"} {
		if _, err := svc.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{CheckpointID: ids[key], Observed: acceptedObserved[key], Technician: "Rui"}); err != nil {
			t.Fatal(err)
		}
	}

	const workers = 8
	var wait sync.WaitGroup
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			_, err := svc.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{
				CheckpointID: ids["high"],
				Observed:     50.05,
				Technician:   "Rui",
			})
			results <- err
		}(i)
	}
	wait.Wait()
	close(results)

	successes, failures := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != workers-1 {
		t.Fatalf("concurrent records: %d success, %d failure, want 1 and %d", successes, failures, workers-1)
	}
	final, err := svc.Case(ctx, caseID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Case.State != domain.CaseReviewReady {
		t.Fatalf("final state = %s, want review_ready", final.Case.State)
	}
	if len(final.Measurements) != 3 {
		t.Fatalf("history = %d measurements, want exactly 3 (one per point)", len(final.Measurements))
	}
	closed := 0
	for _, point := range final.Checkpoints {
		if point.Status == domain.CheckpointAccepted {
			closed++
		}
	}
	if closed != 3 {
		t.Fatalf("closed points = %d, want 3", closed)
	}
}

func TestConcurrentSealCreatesExactlyOne(t *testing.T) {
	svc, ctx := newService()
	view := openThreePointCase(t, svc, ctx, "MET-RACE-SEAL")
	caseID := view.Case.ID
	ids := checkpointIDs(t, view.Checkpoints)
	acceptedObserved := map[string]float64{"low": 10.05, "mid": 25.05, "high": 50.05}
	for _, key := range []string{"low", "mid", "high"} {
		if _, err := svc.RecordMeasurement(ctx, caseID, contracts.RecordMeasurementRequest{CheckpointID: ids[key], Observed: acceptedObserved[key], Technician: "Rui"}); err != nil {
			t.Fatal(err)
		}
	}

	const workers = 8
	var wait sync.WaitGroup
	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			_, err := svc.SealCase(ctx, caseID, contracts.SealCaseRequest{
				Reviewer: "Verifier",
				Summary:  "concurrent attempt",
			})
			results <- err
		}(i)
	}
	wait.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent seals: %d success, want exactly 1", successes)
	}
}

func checkpointIDs(t *testing.T, points []domain.Checkpoint) map[string]string {
	t.Helper()
	ids := make(map[string]string, len(points))
	for _, point := range points {
		ids[point.Label] = point.ID
	}
	for _, label := range []string{"low", "mid", "high"} {
		if ids[label] == "" {
			t.Fatalf("plan missing checkpoint %q", label)
		}
	}
	return ids
}

func findCheckpoint(t *testing.T, points []domain.Checkpoint, id string) domain.Checkpoint {
	t.Helper()
	for _, point := range points {
		if point.ID == id {
			return point
		}
	}
	t.Fatalf("checkpoint %s not found", id)
	return domain.Checkpoint{}
}

func checkpointMap(points []domain.Checkpoint) map[string]domain.Checkpoint {
	result := make(map[string]domain.Checkpoint, len(points))
	for _, point := range points {
		result[point.ID] = point
	}
	return result
}
