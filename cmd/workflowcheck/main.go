package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"example.com/solo-0021-metrology-assurance-suite/internal/bootstrap"
)

type apiResponse struct {
	Status int
	Body   map[string]any
	Raw    string
}

type harness struct {
	baseURL string
	client  *http.Client
	server  *httptest.Server
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to verify")
	flag.Parse()

	checks := map[string]func() error{
		"register-device":          checkRegisterDevice,
		"open-verification-case":   checkOpenVerificationCase,
		"record-measurement":       checkRecordMeasurement,
		"reopen-verification-case": checkReopenVerificationCase,
		"seal-verification":        checkSealVerification,
		"open-multi-point-case":    checkOpenMultiPointCase,
		"record-point-results":     checkRecordPointResults,
		"retest-rejected-point":    checkRetestRejectedPoint,
		"seal-multi-point-case":    checkSealMultiPointCase,
	}
	run, ok := checks[*workflow]
	if !ok {
		fmt.Fprintf(flag.CommandLine.Output(), "unknown workflow %q\n", *workflow)
		flag.Usage()
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Printf("workflow %s: failed: %v\n", *workflow, err)
		os.Exit(1)
	}
	fmt.Printf("workflow %s: ok\n", *workflow)
}

func checkRegisterDevice() error {
	h := newHarness()
	defer h.close()

	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": " met-1001 ",
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return err
	}
	id, err := nestedString(response.Body, "device", "id")
	if err != nil {
		return err
	}
	tag, err := nestedString(response.Body, "device", "asset_tag")
	if err != nil {
		return err
	}
	if tag != "MET-1001" {
		return fmt.Errorf("asset tag was not normalized: %q", tag)
	}

	read, err := h.request(http.MethodGet, "/devices/"+id, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	readID, err := nestedString(read.Body, "device", "id")
	if err != nil {
		return err
	}
	if readID != id {
		return fmt.Errorf("read returned device %q, expected %q", readID, id)
	}

	duplicate, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": "MET-1001",
		"model":     "Other Meter",
		"room":      "Room B",
		"steward":   "Mira",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(duplicate, "duplicate_asset_tag")
}

func checkOpenVerificationCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1101")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	state, err := nestedString(opened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("opened case state = %q", state)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}

	second, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(second, "active_case_exists")
}

func checkRecordMeasurement() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1201")
	if err != nil {
		return err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	accepted, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(accepted, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(accepted.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "review_ready" {
		return fmt.Errorf("accepted measurement state = %q", state)
	}
	acceptedValue, err := nestedBool(accepted.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if !acceptedValue {
		return errors.New("in-tolerance measurement was not accepted")
	}

	second, err := h.recordMeasurement(caseID, 25.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	rejectDeviceID, err := h.createDevice("MET-1202")
	if err != nil {
		return err
	}
	rejectCase, err := h.openCase(rejectDeviceID)
	if err != nil {
		return err
	}
	rejectCaseID, err := nestedString(rejectCase.Body, "case", "id")
	if err != nil {
		return err
	}
	rejected, err := h.recordMeasurement(rejectCaseID, 26.5, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return err
	}
	rejectedState, err := nestedString(rejected.Body, "case", "state")
	if err != nil {
		return err
	}
	if rejectedState != "recheck_required" {
		return fmt.Errorf("out-of-tolerance state = %q", rejectedState)
	}
	rejectedValue, err := nestedBool(rejected.Body, "measurements", 0, "accepted")
	if err != nil {
		return err
	}
	if rejectedValue {
		return errors.New("out-of-tolerance measurement was accepted")
	}
	return nil
}

func checkReopenVerificationCase() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.rejectedCase("MET-1301")
	if err != nil {
		return err
	}
	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "measurement setup was replaced",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(reopened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("reopened case state = %q", state)
	}
	measurements, err := nestedSlice(reopened.Body, "measurements")
	if err != nil {
		return err
	}
	if len(measurements) != 1 {
		return fmt.Errorf("reopen changed measurement history: %d entries", len(measurements))
	}

	second, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "duplicate request",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(second, "state_conflict"); err != nil {
		return err
	}

	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "case_reopened") {
		return errors.New("reopen audit event is missing")
	}
	return nil
}

func checkSealVerification() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.readyCase("MET-1401")
	if err != nil {
		return err
	}
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer": fmt.Sprintf("Reviewer-%d", index+1),
				"summary":  "accepted measurement sealed for review",
			})
			results <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", successes, conflicts)
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}
	state, err := nestedString(read.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "sealed" {
		return fmt.Errorf("sealed case state = %q", state)
	}
	sealID, err := nestedString(read.Body, "seal", "id")
	if err != nil {
		return err
	}
	if sealID == "" {
		return errors.New("evidence seal is missing")
	}
	return nil
}

// checkOpenMultiPointCase verifies the steward defines an ordered measurement
// plan at opening and that bad plans are rejected before a case exists.
func checkOpenMultiPointCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-2101")
	if err != nil {
		return err
	}
	opened, err := h.openMultiPointCase(deviceID, []map[string]any{
		{"label": "low", "nominal": 10.0, "tolerance": 0.1},
		{"label": "mid", "nominal": 25.0, "tolerance": 0.2},
		{"label": "high", "nominal": 50.0, "tolerance": 0.5},
	})
	if err != nil {
		return err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	state, err := nestedString(opened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("opened multi-point case state = %q", state)
	}
	points, err := checkpointsOf(opened.Body)
	if err != nil {
		return err
	}
	if len(points) != 3 {
		return fmt.Errorf("plan has %d checkpoints, expected 3", len(points))
	}
	expected := []struct {
		ordinal   int
		label     string
		nominal   float64
		tolerance float64
	}{
		{1, "low", 10.0, 0.1},
		{2, "mid", 25.0, 0.2},
		{3, "high", 50.0, 0.5},
	}
	for index, want := range expected {
		point, ok := points[index].(map[string]any)
		if !ok {
			return fmt.Errorf("checkpoint %d is not an object", index)
		}
		if got := int(point["ordinal"].(float64)); got != want.ordinal {
			return fmt.Errorf("checkpoint %d ordinal = %d", index, got)
		}
		if got := point["label"].(string); got != want.label {
			return fmt.Errorf("checkpoint %d label = %q", index, got)
		}
		if got := point["nominal"].(float64); got != want.nominal {
			return fmt.Errorf("checkpoint %d nominal = %v", index, got)
		}
		if got := point["tolerance"].(float64); got != want.tolerance {
			return fmt.Errorf("checkpoint %d tolerance = %v", index, got)
		}
		if got := point["status"].(string); got != "pending" {
			return fmt.Errorf("checkpoint %d status = %q, expected pending", index, got)
		}
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}

	// Case-level nominal cannot be mixed with a checkpoint plan.
	mixedDevice, err := h.createDevice("MET-2102")
	if err != nil {
		return err
	}
	mixed, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id":   mixedDevice,
		"nominal":     25.0,
		"tolerance":   0.5,
		"steward":     "Qiao",
		"checkpoints": []map[string]any{{"label": "only", "nominal": 25.0, "tolerance": 0.5}},
	})
	if err != nil {
		return err
	}
	if err := expectStatus(mixed, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(mixed, "invalid_input"); err != nil {
		return err
	}

	// Duplicate point labels are rejected so each result has a unique target.
	duplicateDevice, err := h.createDevice("MET-2103")
	if err != nil {
		return err
	}
	duplicate, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": duplicateDevice,
		"steward":   "Qiao",
		"checkpoints": []map[string]any{
			{"label": "mid", "nominal": 25.0, "tolerance": 0.2},
			{"label": "mid", "nominal": 50.0, "tolerance": 0.5},
		},
	})
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(duplicate, "duplicate_checkpoint"); err != nil {
		return err
	}

	// A non-positive tolerance on a point is rejected.
	badDevice, err := h.createDevice("MET-2104")
	if err != nil {
		return err
	}
	bad, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id":   badDevice,
		"steward":     "Qiao",
		"checkpoints": []map[string]any{{"label": "zero", "nominal": 1.0, "tolerance": 0.0}},
	})
	if err != nil {
		return err
	}
	if err := expectStatus(bad, http.StatusBadRequest); err != nil {
		return err
	}
	return expectErrorCode(bad, "invalid_input")
}

// checkRecordPointResults verifies point-by-point recording: accepted points
// stay open for the others, a failed point blocks the case, a closed point
// rejects duplicate records, and unknown points cannot fabricate work.
func checkRecordPointResults() error {
	h := newHarness()
	defer h.close()

	caseID, points, err := h.threePointCase("MET-2201")
	if err != nil {
		return err
	}
	lowID := pointID(points, "low")
	midID := pointID(points, "mid")
	highID := pointID(points, "high")
	if lowID == "" || midID == "" || highID == "" {
		return errors.New("checkpoint ids missing from plan")
	}

	// First point accepted: case must still be awaiting remaining points.
	first, err := h.recordPoint(caseID, lowID, 10.05, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(first, http.StatusCreated); err != nil {
		return err
	}
	if state, _ := nestedString(first.Body, "case", "state"); state != "awaiting_measurement" {
		return fmt.Errorf("after one accepted point state = %q, expected awaiting_measurement", state)
	}
	firstPoints, err := checkpointsOf(first.Body)
	if err != nil {
		return err
	}
	low := firstPoints[0].(map[string]any)
	if low["status"] != "accepted" || low["measurement_id"] == "" || low["technician"] != "Rui" {
		return fmt.Errorf("accepted low point basis not stored: %v", low)
	}

	// Recording the same completed point again must not fabricate progress.
	dup, err := h.recordPoint(caseID, lowID, 10.02, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(dup, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(dup, "checkpoint_closed"); err != nil {
		return err
	}

	// A point id that does not exist cannot be recorded.
	unknown, err := h.recordPoint(caseID, "checkpoint-9999", 25.0, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(unknown, http.StatusNotFound); err != nil {
		return err
	}
	if err := expectErrorCode(unknown, "not_found"); err != nil {
		return err
	}

	// Failing the middle point blocks the whole case: high must then be locked.
	failed, err := h.recordPoint(caseID, midID, 25.9, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(failed, http.StatusCreated); err != nil {
		return err
	}
	if state, _ := nestedString(failed.Body, "case", "state"); state != "recheck_required" {
		return fmt.Errorf("after rejected point state = %q, expected recheck_required", state)
	}
	failedPoints, err := checkpointsOf(failed.Body)
	if err != nil {
		return err
	}
	failedStatuses := checkpointStatuses(failedPoints)
	if failedStatuses["mid"] != "rejected" {
		return fmt.Errorf("mid point status = %q, expected rejected", failedStatuses["mid"])
	}
	if failedStatuses["low"] != "accepted" {
		return fmt.Errorf("low point status = %q, expected accepted to persist", failedStatuses["low"])
	}
	locked, err := h.recordPoint(caseID, highID, 50.1, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(locked, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(locked, "state_conflict"); err != nil {
		return err
	}

	// Sealing while a point is rejected must fail.
	earlySeal, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
		"reviewer": "Verifier",
		"summary":  "attempt while point rejected",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(earlySeal, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(earlySeal, "state_conflict"); err != nil {
		return err
	}

	// Every attempt remains in the measurement history for traceability.
	history, err := nestedSlice(failed.Body, "measurements")
	if err != nil {
		return err
	}
	if len(history) != 2 {
		return fmt.Errorf("measurement history has %d entries, expected 2", len(history))
	}
	return nil
}

// checkRetestRejectedPoint verifies a reopen only resets rejected points while
// accepted points keep their immutable basis, and that supplementary results
// eventually close the whole case.
func checkRetestRejectedPoint() error {
	h := newHarness()
	defer h.close()

	caseID, points, err := h.threePointCaseWithFailure("MET-2301")
	if err != nil {
		return err
	}
	midID := pointID(points, "mid")
	highID := pointID(points, "high")

	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "probe was re-seated after failed mid point",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopened, http.StatusOK); err != nil {
		return err
	}
	if state, _ := nestedString(reopened.Body, "case", "state"); state != "awaiting_measurement" {
		return fmt.Errorf("reopened state = %q", state)
	}
	after, err := checkpointsOf(reopened.Body)
	if err != nil {
		return err
	}
	statusByLabel := checkpointStatuses(after)
	if statusByLabel["low"] != "accepted" {
		return fmt.Errorf("accepted low point was disturbed: %q", statusByLabel["low"])
	}
	if statusByLabel["mid"] != "pending" {
		return fmt.Errorf("rejected mid point was not reset: %q", statusByLabel["mid"])
	}
	if statusByLabel["high"] != "pending" {
		return fmt.Errorf("pending high point changed state: %q", statusByLabel["high"])
	}

	// The rejected attempt is preserved in history even after reset.
	history, err := nestedSlice(reopened.Body, "measurements")
	if len(history) != 2 {
		return fmt.Errorf("history after reopen has %d entries, expected 2", len(history))
	}

	// Supplementary mid measurement accepted; then the last point closes plan.
	retest, err := h.recordPoint(caseID, midID, 25.05, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(retest, http.StatusCreated); err != nil {
		return err
	}
	if state, _ := nestedString(retest.Body, "case", "state"); state != "awaiting_measurement" {
		return fmt.Errorf("after retested mid state = %q", state)
	}
	final, err := h.recordPoint(caseID, highID, 50.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(final, http.StatusCreated); err != nil {
		return err
	}
	if state, _ := nestedString(final.Body, "case", "state"); state != "review_ready" {
		return fmt.Errorf("after final point state = %q, expected review_ready", state)
	}

	// Events preserve the original rejection, the reopen, and every record.
	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "case_reopened") {
		return errors.New("reopen audit event is missing")
	}
	rejected := countEvents(events.Body, "measurement_rejected")
	accepted := countEvents(events.Body, "measurement_accepted")
	if rejected != 1 || accepted != 3 {
		return fmt.Errorf("event counts accepted=3 rejected=1 expected, got accepted=%d rejected=%d", accepted, rejected)
	}
	return nil
}

// checkSealMultiPointCase verifies sealing is possible only once all points
// close, the seal freezes every point basis, and concurrent completion/sealing
// cannot produce a forged or double complete package.
func checkSealMultiPointCase() error {
	h := newHarness()
	defer h.close()

	// Concurrent technicians racing the same last pending point: exactly one
	// record lands; the duplicate is rejected and the case completes once.
	raceCaseID, racePoints, err := h.threePointCaseAcceptedFirstTwo("MET-2401")
	if err != nil {
		return err
	}
	lastID := pointID(racePoints, "high")
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make(chan outcome, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response, err := h.recordPoint(raceCaseID, lastID, 50.05, fmt.Sprintf("Tech-%d", index+1))
			results <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for item := range results {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			successes++
			if state, _ := nestedString(item.response.Body, "case", "state"); state != "review_ready" {
				return fmt.Errorf("winning record did not complete the plan: %q", state)
			}
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent record status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent record outcomes: %d success, %d conflict", successes, conflicts)
	}

	// Full happy path: all three points close and one seal freezes all bases.
	caseID, points, err := h.threePointCase("MET-2402")
	if err != nil {
		return err
	}
	if _, err := h.recordPoint(caseID, pointID(points, "low"), 10.03, "Rui"); err != nil {
		return err
	}
	if _, err := h.recordPoint(caseID, pointID(points, "mid"), 25.10, "Rui"); err != nil {
		return err
	}
	if _, err := h.recordPoint(caseID, pointID(points, "high"), 49.80, "Rui"); err != nil {
		return err
	}
	ready, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if state, _ := nestedString(ready.Body, "case", "state"); state != "review_ready" {
		return fmt.Errorf("ready case state = %q", state)
	}

	sealResults := make(chan outcome, 2)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response, err := h.request(http.MethodPost, "/cases/"+caseID+"/seal", map[string]any{
				"reviewer": fmt.Sprintf("Verifier-%d", index+1),
				"summary":  "three point evidence package",
			})
			sealResults <- outcome{response: response, err: err}
		}(index)
	}
	wait.Wait()
	close(sealResults)
	sealed, sealConflicts := 0, 0
	var sealedBody map[string]any
	for item := range sealResults {
		if item.err != nil {
			return item.err
		}
		switch item.response.Status {
		case http.StatusCreated:
			sealed++
			sealedBody = item.response.Body
		case http.StatusConflict:
			sealConflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if sealed != 1 || sealConflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", sealed, sealConflicts)
	}

	frozen, err := nestedSlice(sealedBody["seal"].(map[string]any), "points")
	if err != nil {
		return err
	}
	if len(frozen) != 3 {
		return fmt.Errorf("seal freezes %d points, expected 3", len(frozen))
	}
	wantLabels := map[string]bool{"low": false, "mid": false, "high": false}
	for _, entry := range frozen {
		point, ok := entry.(map[string]any)
		if !ok {
			return errors.New("frozen point is not an object")
		}
		label := point["label"].(string)
		wantLabels[label] = true
		if point["measurement_id"] == "" || point["technician"] == "" {
			return fmt.Errorf("frozen point %q is missing its basis", label)
		}
	}
	for label, found := range wantLabels {
		if !found {
			return fmt.Errorf("seal is missing point basis %q", label)
		}
	}

	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if state, _ := nestedString(read.Body, "case", "state"); state != "sealed" {
		return fmt.Errorf("sealed state = %q", state)
	}

	// A sealed case rejects further point records.
	after, err := h.recordPoint(caseID, pointID(points, "low"), 10.09, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(after, http.StatusConflict); err != nil {
		return err
	}
	return expectErrorCode(after, "state_conflict")
}

func newHarness() *harness {
	server := httptest.NewServer(bootstrap.NewHandler())
	return &harness{
		baseURL: server.URL,
		client:  server.Client(),
		server:  server,
	}
}

func (h *harness) close() {
	h.server.Close()
}

func (h *harness) request(method, path string, body any) (apiResponse, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return apiResponse{}, err
		}
		reader = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, reader)
	if err != nil {
		return apiResponse{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.client.Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer response.Body.Close()
	rawBytes, err := io.ReadAll(response.Body)
	if err != nil {
		return apiResponse{}, err
	}
	result := apiResponse{Status: response.StatusCode, Raw: string(rawBytes)}
	if len(bytes.TrimSpace(rawBytes)) > 0 {
		if err := json.Unmarshal(rawBytes, &result.Body); err != nil {
			return apiResponse{}, fmt.Errorf("decode %s %s response: %w", method, path, err)
		}
	}
	return result, nil
}

func (h *harness) createDevice(tag string) (string, error) {
	response, err := h.request(http.MethodPost, "/devices", map[string]any{
		"asset_tag": tag,
		"model":     "Bench Meter",
		"room":      "Room A",
		"steward":   "Lin",
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(response.Body, "device", "id")
}

func (h *harness) openCase(deviceID string) (apiResponse, error) {
	response, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id": deviceID,
		"nominal":   25.0,
		"tolerance": 0.5,
		"steward":   "Qiao",
	})
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) recordMeasurement(caseID string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed":   observed,
		"technician": technician,
	})
}

func (h *harness) openMultiPointCase(deviceID string, points []map[string]any) (apiResponse, error) {
	response, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id":   deviceID,
		"steward":     "Qiao",
		"checkpoints": points,
	})
	if err != nil {
		return apiResponse{}, err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) recordPoint(caseID, checkpointID string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"checkpoint_id": checkpointID,
		"observed":      observed,
		"technician":    technician,
	})
}

// threePointCase opens a low/mid/high plan on a fresh device and returns its
// id and the ordered checkpoints.
func (h *harness) threePointCase(tag string) (string, []any, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", nil, err
	}
	opened, err := h.openMultiPointCase(deviceID, []map[string]any{
		{"label": "low", "nominal": 10.0, "tolerance": 0.1},
		{"label": "mid", "nominal": 25.0, "tolerance": 0.2},
		{"label": "high", "nominal": 50.0, "tolerance": 0.5},
	})
	if err != nil {
		return "", nil, err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", nil, err
	}
	points, err := checkpointsOf(opened.Body)
	if err != nil {
		return "", nil, err
	}
	return caseID, points, nil
}

// threePointCaseWithFailure reaches recheck_required with low accepted and mid
// rejected; high stays pending.
func (h *harness) threePointCaseWithFailure(tag string) (string, []any, error) {
	caseID, points, err := h.threePointCase(tag)
	if err != nil {
		return "", nil, err
	}
	if _, err := h.recordPoint(caseID, pointID(points, "low"), 10.05, "Rui"); err != nil {
		return "", nil, err
	}
	rejected, err := h.recordPoint(caseID, pointID(points, "mid"), 25.9, "Rui")
	if err != nil {
		return "", nil, err
	}
	if err := expectStatus(rejected, http.StatusCreated); err != nil {
		return "", nil, err
	}
	points, err = checkpointsOf(rejected.Body)
	if err != nil {
		return "", nil, err
	}
	return caseID, points, nil
}

// threePointCaseAcceptedFirstTwo leaves only high pending, in review-eligible
// awaiting state.
func (h *harness) threePointCaseAcceptedFirstTwo(tag string) (string, []any, error) {
	caseID, points, err := h.threePointCase(tag)
	if err != nil {
		return "", nil, err
	}
	if _, err := h.recordPoint(caseID, pointID(points, "low"), 10.04, "Rui"); err != nil {
		return "", nil, err
	}
	mid, err := h.recordPoint(caseID, pointID(points, "mid"), 24.9, "Rui")
	if err != nil {
		return "", nil, err
	}
	if err := expectStatus(mid, http.StatusCreated); err != nil {
		return "", nil, err
	}
	points, err = checkpointsOf(mid.Body)
	if err != nil {
		return "", nil, err
	}
	return caseID, points, nil
}

func checkpointsOf(body map[string]any) ([]any, error) {
	return nestedSlice(body, "checkpoints")
}

func pointID(points []any, label string) string {
	for _, entry := range points {
		point, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if point["label"] == label {
			id, _ := point["id"].(string)
			return id
		}
	}
	return ""
}

func checkpointStatuses(points []any) map[string]string {
	result := make(map[string]string, len(points))
	for _, entry := range points {
		point, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		label, _ := point["label"].(string)
		status, _ := point["status"].(string)
		result[label] = status
	}
	return result
}

func countEvents(body map[string]any, kind string) int {
	items, err := nestedSlice(body, "events")
	if err != nil {
		return 0
	}
	count := 0
	for _, item := range items {
		event, ok := item.(map[string]any)
		if ok && event["kind"] == kind {
			count++
		}
	}
	return count
}

func (h *harness) rejectedCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 26.5, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func (h *harness) readyCase(tag string) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCase(deviceID)
	if err != nil {
		return "", err
	}
	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return "", err
	}
	response, err := h.recordMeasurement(caseID, 25.2, "Rui")
	if err != nil {
		return "", err
	}
	if err := expectStatus(response, http.StatusCreated); err != nil {
		return "", err
	}
	return caseID, nil
}

func expectStatus(response apiResponse, expected int) error {
	if response.Status != expected {
		return fmt.Errorf("status = %d, expected %d: %s", response.Status, expected, response.Raw)
	}
	return nil
}

func expectErrorCode(response apiResponse, expected string) error {
	code, err := nestedString(response.Body, "error", "code")
	if err != nil {
		return err
	}
	if code != expected {
		return fmt.Errorf("error code = %q, expected %q", code, expected)
	}
	return nil
}

func nestedString(root map[string]any, path ...string) (string, error) {
	value, err := nestedValue(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", strings.Join(path, "."))
	}
	return text, nil
}

func nestedBool(root map[string]any, path ...any) (bool, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return false, err
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%v is not a boolean", path)
	}
	return flag, nil
}

func nestedSlice(root map[string]any, key string) ([]any, error) {
	value, ok := root[key]
	if !ok {
		return nil, fmt.Errorf("missing %s", key)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array", key)
	}
	return items, nil
}

func hasEvent(root map[string]any, kind string) bool {
	items, err := nestedSlice(root, "events")
	if err != nil {
		return false
	}
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if event["kind"] == kind {
			return true
		}
	}
	return false
}

func nestedValue(root map[string]any, path ...string) (any, error) {
	parts := make([]any, 0, len(path))
	for _, part := range path {
		parts = append(parts, part)
	}
	return nestedAny(root, parts...)
}

func nestedAny(root map[string]any, path ...any) (any, error) {
	var current any = root
	for _, part := range path {
		switch typed := part.(type) {
		case string:
			object, ok := current.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s is not an object", typed)
			}
			value, ok := object[typed]
			if !ok {
				return nil, fmt.Errorf("missing %s", typed)
			}
			current = value
		case int:
			items, ok := current.([]any)
			if !ok {
				return nil, fmt.Errorf("index %d is not inside an array", typed)
			}
			if typed < 0 || typed >= len(items) {
				return nil, fmt.Errorf("index %d is out of range", typed)
			}
			current = items[typed]
		default:
			return nil, fmt.Errorf("unsupported path element %T", part)
		}
	}
	return current, nil
}
