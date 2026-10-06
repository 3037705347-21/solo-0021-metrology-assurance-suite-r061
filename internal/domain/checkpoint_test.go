package domain

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

func samplePlan() []Checkpoint {
	low, _ := NewCheckpoint("checkpoint-0001", "case-0001", "low", 1, 10, 0.1)
	mid, _ := NewCheckpoint("checkpoint-0002", "case-0001", "mid", 2, 25, 0.2)
	high, _ := NewCheckpoint("checkpoint-0003", "case-0001", "high", 3, 50, 0.5)
	return []Checkpoint{low, mid, high}
}

func TestNewCheckpointValidation(t *testing.T) {
	t.Run("rejects zero tolerance", func(t *testing.T) {
		_, err := NewCheckpoint("p1", "c1", "low", 1, 10, 0)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("expected invalid input, got %v", err)
		}
	})
	t.Run("rejects blank label", func(t *testing.T) {
		_, err := NewCheckpoint("p1", "c1", "  ", 1, 10, 0.1)
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("expected invalid input, got %v", err)
		}
	})
	t.Run("starts pending without basis", func(t *testing.T) {
		point, err := NewCheckpoint("p1", "c1", "low", 1, 10, 0.1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if point.Status != CheckpointPending || point.MeasurementID != "" {
			t.Fatalf("new checkpoint not pending and empty: %+v", point)
		}
	})
}

func TestCheckpointRecordCannotBeRepeated(t *testing.T) {
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	point, _ := NewCheckpoint("p1", "c1", "low", 1, 10, 0.1)
	if err := point.Record("measurement-0001", "Rui", true, at); err != nil {
		t.Fatalf("first record: %v", err)
	}
	if point.Status != CheckpointAccepted || point.MeasurementID != "measurement-0001" {
		t.Fatalf("basis not stored: %+v", point)
	}
	if err := point.Record("measurement-0002", "Rui", true, at.Add(time.Second)); !errors.Is(err, ErrCheckpointComplete) {
		t.Fatalf("duplicate record error = %v, want checkpoint complete", err)
	}
	if point.MeasurementID != "measurement-0001" {
		t.Fatalf("basis was overwritten by duplicate record: %s", point.MeasurementID)
	}
}

func TestCheckpointResetOnlyFromRejected(t *testing.T) {
	at := time.Now()
	rejected, _ := NewCheckpoint("p1", "c1", "low", 1, 10, 0.1)
	if err := rejected.Record("m1", "Rui", false, at); err != nil {
		t.Fatal(err)
	}
	if err := rejected.Reset(); err != nil {
		t.Fatalf("reset rejected point: %v", err)
	}
	if rejected.Status != CheckpointPending || rejected.MeasurementID != "" || rejected.RecordedAt != nil {
		t.Fatalf("reset did not clear basis: %+v", rejected)
	}

	accepted, _ := NewCheckpoint("p2", "c1", "mid", 2, 25, 0.2)
	if err := accepted.Record("m2", "Rui", true, at); err != nil {
		t.Fatal(err)
	}
	if err := accepted.Reset(); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("reset accepted point error = %v, want state conflict", err)
	}
	if accepted.MeasurementID != "m2" {
		t.Fatalf("accepted basis lost after failed reset: %+v", accepted)
	}
}

func TestCaseAggregatesCheckpointStates(t *testing.T) {
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

	t.Run("partial acceptance stays awaiting", func(t *testing.T) {
		c, _ := NewVerificationCase("c1", "d1", 0, 0, "Qiao", at)
		points := samplePlan()
		if err := c.RecordCheckpoint(points, &points[0], "m1", "Rui", true, at); err != nil {
			t.Fatal(err)
		}
		if c.State != CaseAwaitingMeasurement {
			t.Fatalf("state = %s, want awaiting", c.State)
		}
		if c.Version != 2 {
			t.Fatalf("version = %d, want 2", c.Version)
		}
	})

	t.Run("one rejection blocks the case", func(t *testing.T) {
		c, _ := NewVerificationCase("c1", "d1", 0, 0, "Qiao", at)
		points := samplePlan()
		if err := c.RecordCheckpoint(points, &points[0], "m1", "Rui", true, at); err != nil {
			t.Fatal(err)
		}
		if err := c.RecordCheckpoint(points, &points[1], "m2", "Rui", false, at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if c.State != CaseRecheckRequired {
			t.Fatalf("state = %s, want recheck_required", c.State)
		}
		// Remaining points cannot close while blocked.
		if err := c.RecordCheckpoint(points, &points[2], "m3", "Rui", true, at.Add(2*time.Second)); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("blocked record error = %v, want state conflict", err)
		}
		if points[2].Status != CheckpointPending {
			t.Fatalf("blocked point changed to %s", points[2].Status)
		}
	})

	t.Run("all acceptance reaches review", func(t *testing.T) {
		c, _ := NewVerificationCase("c1", "d1", 0, 0, "Qiao", at)
		points := samplePlan()
		for i := range points {
			if err := c.RecordCheckpoint(points, &points[i], fmt.Sprintf("m%d", i+1), "Rui", true, at.Add(time.Duration(i)*time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		if c.State != CaseReviewReady {
			t.Fatalf("state = %s, want review_ready", c.State)
		}
	})

	t.Run("duplicate point record reports state conflict after completion", func(t *testing.T) {
		c, _ := NewVerificationCase("c1", "d1", 0, 0, "Qiao", at)
		points := samplePlan()
		if err := c.RecordCheckpoint(points, &points[0], "m1", "Rui", true, at); err != nil {
			t.Fatal(err)
		}
		// Same point again while case still awaits others: checkpoint closed.
		if err := c.RecordCheckpoint(points, &points[0], "m2", "Rui", true, at.Add(time.Second)); !errors.Is(err, ErrCheckpointComplete) {
			t.Fatalf("duplicate point error = %v, want checkpoint complete", err)
		}
	})
}

func TestReopenResetsOnlyRejectedPoints(t *testing.T) {
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	c, _ := NewVerificationCase("c1", "d1", 0, 0, "Qiao", at)
	points := samplePlan()
	_ = c.RecordCheckpoint(points, &points[0], "m1", "Rui", true, at)
	_ = c.RecordCheckpoint(points, &points[1], "m2", "Rui", false, at.Add(time.Second))

	reset, err := c.Reopen(points, at.Add(2*time.Second))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if c.State != CaseAwaitingMeasurement {
		t.Fatalf("state = %s", c.State)
	}
	if len(reset) != 1 || reset[0] != "mid" {
		t.Fatalf("reset labels = %v, want [mid]", reset)
	}
	if points[0].Status != CheckpointAccepted || points[0].MeasurementID != "m1" {
		t.Fatalf("accepted point basis disturbed: %+v", points[0])
	}
	if points[1].Status != CheckpointPending || points[1].MeasurementID != "" {
		t.Fatalf("rejected point not cleared: %+v", points[1])
	}

	// Second reopen with no rejected point is invalid.
	if _, err := c.Reopen(points, at.Add(3*time.Second)); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("second reopen error = %v, want state conflict", err)
	}
}

func TestSealRequiresEveryPointAccepted(t *testing.T) {
	at := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

	t.Run("pending point blocks seal even in awaiting state", func(t *testing.T) {
		c, _ := NewVerificationCase("c1", "d1", 0, 0, "Qiao", at)
		points := samplePlan()
		if err := c.Seal(points, at); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("seal awaiting error = %v, want state conflict", err)
		}
	})

	t.Run("seals when all accepted and is terminal", func(t *testing.T) {
		c, _ := NewVerificationCase("c1", "d1", 0, 0, "Qiao", at)
		points := samplePlan()
		for i := range points {
			_ = c.RecordCheckpoint(points, &points[i], fmt.Sprintf("m%d", i+1), "Rui", true, at.Add(time.Duration(i)*time.Second))
		}
		if err := c.Seal(points, at.Add(time.Minute)); err != nil {
			t.Fatalf("seal: %v", err)
		}
		if c.State != CaseSealed || c.SealedAt == nil {
			t.Fatalf("seal did not close case: %+v", c)
		}
		if err := c.Seal(points, at.Add(2*time.Minute)); !errors.Is(err, ErrStateConflict) {
			t.Fatalf("second seal error = %v, want state conflict", err)
		}
	})
}

func TestMeasurementUsesCheckpointBasis(t *testing.T) {
	at := time.Now()
	m, err := NewMeasurement("m1", "c1", "checkpoint-0002", "mid", 25.15, 25, 0.2, "Rui", at)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(m.Deviation-0.15) > 1e-12 || !m.Accepted {
		t.Fatalf("deviation = %v accepted = %v", m.Deviation, m.Accepted)
	}
	out, err := NewMeasurement("m2", "c1", "checkpoint-0002", "mid", 25.30, 25, 0.2, "Rui", at)
	if err != nil {
		t.Fatal(err)
	}
	if out.Accepted {
		t.Fatalf("out-of-tolerance value was accepted: deviation %v", out.Deviation)
	}
}
