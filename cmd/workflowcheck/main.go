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
	"example.com/solo-0021-metrology-assurance-suite/internal/domain"
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
		"register-device":            checkRegisterDevice,
		"open-verification-case":     checkOpenVerificationCase,
		"record-measurement":         checkRecordMeasurement,
		"reopen-verification-case":   checkReopenVerificationCase,
		"seal-verification":          checkSealVerification,
		"open-multi-checkpoint-case": checkOpenMultiCheckpointCase,
		"record-checkpoint-results":  checkRecordCheckpointResults,
		"rework-checkpoint":          checkReworkCheckpoint,
		"seal-checkpoint-package":    checkSealCheckpointPackage,
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
	if err := expectErrorCode(second, "checkpoint_already_recorded"); err != nil {
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

// checkOpenMultiCheckpointCase proves that a steward fixes the full
// measurement plan at case opening: multiple points, each with its own nominal
// value and tolerance, are returned in order, and malformed plans never open a
// case.
func checkOpenMultiCheckpointCase() error {
	h := newHarness()
	defer h.close()

	deviceID, err := h.createDevice("MET-1501")
	if err != nil {
		return err
	}
	opened, err := h.openCasePlan(deviceID, []map[string]any{
		{"code": "p1", "label": "low range", "nominal": 10.0, "tolerance": 0.1},
		{"code": "p2", "label": "mid range", "nominal": 25.0, "tolerance": 0.5},
		{"code": "p3", "label": "high range", "nominal": 50.0, "tolerance": 1.0},
	})
	if err != nil {
		return err
	}
	if err := expectStatus(opened, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(opened.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("multi-point case state = %q", state)
	}
	points, err := nestedObjectSlice(opened.Body, "case", "checkpoints")
	if err != nil {
		return err
	}
	if len(points) != 3 {
		return fmt.Errorf("plan returned %d checkpoints, expected 3", len(points))
	}
	codes := [3]string{"p1", "p2", "p3"}
	for index, code := range codes {
		got, ok := points[index]["code"].(string)
		if !ok || got != code {
			return fmt.Errorf("checkpoint %d code = %v, expected %q", index, points[index]["code"], code)
		}
		status, ok := points[index]["status"].(string)
		if !ok || status != "pending" {
			return fmt.Errorf("checkpoint %s status = %v, expected pending", code, points[index]["status"])
		}
		if measured, ok := points[index]["measurement_id"].(string); ok && measured != "" {
			return fmt.Errorf("new checkpoint %s already cites a measurement", code)
		}
	}
	midNominal, err := nestedFloat(opened.Body, "case", "checkpoints", 1, "nominal")
	if err != nil {
		return err
	}
	if midNominal != 25.0 {
		return fmt.Errorf("p2 nominal = %v", midNominal)
	}

	caseID, err := nestedString(opened.Body, "case", "id")
	if err != nil {
		return err
	}
	read, err := h.request(http.MethodGet, "/cases/"+caseID, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(read, http.StatusOK); err != nil {
		return err
	}

	duplicate, err := h.createDevice("MET-1502")
	if err != nil {
		return err
	}
	repeatedCode, err := h.openCasePlan(duplicate, []map[string]any{
		{"code": "p1", "nominal": 10.0, "tolerance": 0.1},
		{"code": "p1", "nominal": 25.0, "tolerance": 0.5},
	})
	if err != nil {
		return err
	}
	if err := expectStatus(repeatedCode, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(repeatedCode, "invalid_input"); err != nil {
		return err
	}

	empty, err := h.openCasePlan(duplicate, []map[string]any{})
	if err != nil {
		return err
	}
	if err := expectStatus(empty, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(empty, "invalid_input"); err != nil {
		return err
	}

	badTolerance, err := h.openCasePlan(duplicate, []map[string]any{
		{"code": "p1", "nominal": 10.0, "tolerance": 0},
	})
	if err != nil {
		return err
	}
	if err := expectStatus(badTolerance, http.StatusBadRequest); err != nil {
		return err
	}
	if err := expectErrorCode(badTolerance, "invalid_input"); err != nil {
		return err
	}

	legacyDevice, err := h.createDevice("MET-1503")
	if err != nil {
		return err
	}
	legacy, err := h.openCase(legacyDevice)
	if err != nil {
		return err
	}
	legacyPoints, err := nestedObjectSlice(legacy.Body, "case", "checkpoints")
	if err != nil {
		return err
	}
	if len(legacyPoints) != 1 || legacyPoints[0]["code"] != domain.DefaultCheckpointCode {
		return fmt.Errorf("legacy case did not synthesize one default checkpoint: %v", legacyPoints)
	}
	return nil
}

// checkRecordCheckpointResults proves technicians close points one at a time
// against the plan: points accept one result each, the case stays open while
// points remain, a failed point blocks further measurement, and the case only
// becomes review_ready once every point passes.
func checkRecordCheckpointResults() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.openThreePointCase("MET-1601", h)
	if err != nil {
		return err
	}

	first, err := h.recordCheckpoint(caseID, "p1", 10.05, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(first, http.StatusCreated); err != nil {
		return err
	}
	state, err := nestedString(first.Body, "case", "state")
	if err != nil {
		return err
	}
	if state != "awaiting_measurement" {
		return fmt.Errorf("case state after 1/3 points = %q", state)
	}
	p1, err := checkpointByCode(first.Body, "p1")
	if err != nil {
		return err
	}
	if p1["status"] != "passed" {
		return fmt.Errorf("p1 status = %v", p1["status"])
	}
	measurementID, ok := p1["measurement_id"].(string)
	if !ok || measurementID == "" {
		return fmt.Errorf("passed p1 does not cite its measurement: %v", p1)
	}
	last, err := latestMeasurement(first.Body, measurementID)
	if err != nil {
		return err
	}
	if last["checkpoint_id"] != p1["id"] {
		return fmt.Errorf("measurement %s is not bound to checkpoint p1", measurementID)
	}

	duplicate, err := h.recordCheckpoint(caseID, "p1", 10.02, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(duplicate, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(duplicate, "checkpoint_already_recorded"); err != nil {
		return err
	}

	unknown, err := h.recordCheckpoint(caseID, "missing", 1.0, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(unknown, http.StatusNotFound); err != nil {
		return err
	}
	if err := expectErrorCode(unknown, "checkpoint_not_found"); err != nil {
		return err
	}

	second, err := h.recordCheckpoint(caseID, "p2", 25.2, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(second, http.StatusCreated); err != nil {
		return err
	}

	third, err := h.recordCheckpoint(caseID, "p3", 52.0, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(third, http.StatusCreated); err != nil {
		return err
	}
	failedState, err := nestedString(third.Body, "case", "state")
	if err != nil {
		return err
	}
	if failedState != "recheck_required" {
		return fmt.Errorf("case state after a failed point = %q", failedState)
	}

	blocked, err := h.recordCheckpoint(caseID, "p2", 25.0, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(blocked, http.StatusConflict); err != nil {
		return err
	}
	// Even though p2 is already passed, the failed p3 blocks the whole case:
	// the steward's reopen gate fires before the per-point duplicate guard.
	if err := expectErrorCode(blocked, "state_conflict"); err != nil {
		return err
	}

	// A second technician cannot sneak the whole package through by racing
	// retries against a case that is still in recheck.
	blockedRetry, err := h.recordCheckpoint(caseID, "p3", 50.1, "Min")
	if err != nil {
		return err
	}
	if err := expectStatus(blockedRetry, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(blockedRetry, "state_conflict"); err != nil {
		return err
	}

	// Concurrent advancement must stay honest: two technicians racing the
	// same pending point produce exactly one accepted record, while distinct
	// points recorded in parallel all close and the package becomes review
	// ready only when every point is genuinely done.
	raceCaseID, err := h.openThreePointCase("MET-1602", h)
	if err != nil {
		return err
	}
	duel := make(chan apiResponse, 2)
	var duelWait sync.WaitGroup
	for index := 0; index < 2; index++ {
		duelWait.Add(1)
		go func(index int) {
			defer duelWait.Done()
			response, raceErr := h.recordCheckpoint(raceCaseID, "p1", 10.02, fmt.Sprintf("Rui-%d", index+1))
			if raceErr != nil {
				duel <- apiResponse{Status: -1}
				return
			}
			duel <- response
		}(index)
	}
	duelWait.Wait()
	close(duel)
	duelWins, duelConflicts := 0, 0
	for response := range duel {
		switch response.Status {
		case http.StatusCreated:
			duelWins++
		case http.StatusConflict:
			duelConflicts++
		default:
			return fmt.Errorf("unexpected concurrent duplicate status %d", response.Status)
		}
	}
	if duelWins != 1 || duelConflicts != 1 {
		return fmt.Errorf("same-point race outcomes: %d success, %d conflict", duelWins, duelConflicts)
	}

	parallel := make(chan apiResponse, 2)
	var parallelWait sync.WaitGroup
	for _, point := range []struct {
		code     string
		observed float64
	}{{"p2", 25.2}, {"p3", 50.2}} {
		parallelWait.Add(1)
		go func(code string, observed float64) {
			defer parallelWait.Done()
			response, raceErr := h.recordCheckpoint(raceCaseID, code, observed, "Rui")
			if raceErr != nil {
				parallel <- apiResponse{Status: -1}
				return
			}
			parallel <- response
		}(point.code, point.observed)
	}
	parallelWait.Wait()
	close(parallel)
	for response := range parallel {
		if response.Status != http.StatusCreated {
			return fmt.Errorf("distinct-point race returned status %d", response.Status)
		}
	}
	readRace, err := h.request(http.MethodGet, "/cases/"+raceCaseID, nil)
	if err != nil {
		return err
	}
	raceState, err := nestedString(readRace.Body, "case", "state")
	if err != nil {
		return err
	}
	if raceState != "review_ready" {
		return fmt.Errorf("parallel case state = %q, expected review_ready", raceState)
	}
	raceHistory, err := nestedSlice(readRace.Body, "measurements")
	if err != nil {
		return err
	}
	if len(raceHistory) != 3 {
		return fmt.Errorf("parallel closing produced %d measurements, expected 3", len(raceHistory))
	}
	return nil
}

// checkReworkCheckpoint proves repair work reopens only the failed point: a
// passed point and its evidence survive rework, the failed measurement history
// is preserved, and the closed point cannot be recorded twice during the
// retry.
func checkReworkCheckpoint() error {
	h := newHarness()
	defer h.close()

	caseID, err := h.openThreePointCase("MET-1701", h)
	if err != nil {
		return err
	}
	if _, err := h.recordCheckpoint(caseID, "p1", 10.05, "Rui"); err != nil {
		return err
	}
	if _, err := h.recordCheckpoint(caseID, "p2", 25.2, "Rui"); err != nil {
		return err
	}
	failed, err := h.recordCheckpoint(caseID, "p3", 52.0, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(failed, http.StatusCreated); err != nil {
		return err
	}
	failedMeasurementID, err := nestedPathString(failed.Body, "case", "checkpoints", 2, "measurement_id")
	if err != nil {
		return err
	}
	beforeMeasurements, err := nestedSlice(failed.Body, "measurements")
	if err != nil {
		return err
	}
	if len(beforeMeasurements) != 3 {
		return fmt.Errorf("expected 3 measurements before reopen, got %d", len(beforeMeasurements))
	}

	reopened, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "high-range probe was replaced",
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
		return fmt.Errorf("reopened state = %q", state)
	}
	p1, err := checkpointByCode(reopened.Body, "p1")
	if err != nil {
		return err
	}
	if p1["status"] != "passed" {
		return fmt.Errorf("passed p1 was reset by reopen: %v", p1["status"])
	}
	p2, err := checkpointByCode(reopened.Body, "p2")
	if err != nil {
		return err
	}
	if p2["status"] != "passed" {
		return fmt.Errorf("passed p2 was reset by reopen: %v", p2["status"])
	}
	p3, err := checkpointByCode(reopened.Body, "p3")
	if err != nil {
		return err
	}
	if p3["status"] != "pending" {
		return fmt.Errorf("failed p3 status after reopen = %v", p3["status"])
	}
	attempts, err := checkpointFloat(reopened.Body, "p3", "attempts")
	if err != nil {
		return err
	}
	if attempts != 1 {
		return fmt.Errorf("p3 attempts after reopen = %v, expected 1 to remain traceable", attempts)
	}
	history, err := nestedSlice(reopened.Body, "measurements")
	if err != nil {
		return err
	}
	if len(history) != 3 {
		return fmt.Errorf("reopen changed measurement history: %d entries", len(history))
	}
	if !measurementExists(history, failedMeasurementID) {
		return errors.New("failed measurement disappeared from history after reopen")
	}

	secondReopen, err := h.request(http.MethodPost, "/cases/"+caseID+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "duplicate reopen",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(secondReopen, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(secondReopen, "state_conflict"); err != nil {
		return err
	}

	events, err := h.request(http.MethodGet, "/cases/"+caseID+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if !hasEvent(events.Body, "checkpoint_failed") || !hasEvent(events.Body, "case_reopened") {
		return errors.New("rework audit trail is missing the failed-point or reopen event")
	}

	retried, err := h.recordCheckpoint(caseID, "p3", 50.2, "Min")
	if err != nil {
		return err
	}
	if err := expectStatus(retried, http.StatusCreated); err != nil {
		return err
	}
	readyState, err := nestedString(retried.Body, "case", "state")
	if err != nil {
		return err
	}
	if readyState != "review_ready" {
		return fmt.Errorf("state after retry = %q, expected review_ready", readyState)
	}
	p3After, err := checkpointByCode(retried.Body, "p3")
	if err != nil {
		return err
	}
	if p3After["status"] != "passed" {
		return fmt.Errorf("retried p3 status = %v", p3After["status"])
	}
	if p3After["measurement_id"] == failedMeasurementID {
		return errors.New("retried p3 still cites the failed measurement")
	}
	attemptsAfter, err := checkpointFloat(retried.Body, "p3", "attempts")
	if err != nil {
		return err
	}
	if attemptsAfter != 2 {
		return fmt.Errorf("p3 attempts after retry = %v, expected 2", attemptsAfter)
	}
	finalHistory, err := nestedSlice(retried.Body, "measurements")
	if err != nil {
		return err
	}
	if len(finalHistory) != 4 || !measurementExists(finalHistory, failedMeasurementID) {
		return fmt.Errorf("retry did not preserve the failed attempt: %d measurements", len(finalHistory))
	}
	return nil
}

// checkSealCheckpointPackage proves the whole package is one evidence bundle:
// sealing is blocked while any point is open or failed, the seal freezes every
// point with its measurement basis, and concurrent sealers cannot produce more
// than one seal.
func checkSealCheckpointPackage() error {
	h := newHarness()
	defer h.close()

	early, err := h.openThreePointCase("MET-1801", h)
	if err != nil {
		return err
	}
	blocked, err := h.request(http.MethodPost, "/cases/"+early+"/seal", map[string]any{
		"reviewer": "Verifier",
		"summary":  "premature seal attempt",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(blocked, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(blocked, "state_conflict"); err != nil {
		return err
	}

	if _, err := h.recordCheckpoint(early, "p1", 10.05, "Rui"); err != nil {
		return err
	}
	if _, err := h.recordCheckpoint(early, "p2", 25.2, "Rui"); err != nil {
		return err
	}
	stillOpen, err := h.request(http.MethodPost, "/cases/"+early+"/seal", map[string]any{
		"reviewer": "Verifier",
		"summary":  "one point still pending",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(stillOpen, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(stillOpen, "state_conflict"); err != nil {
		return err
	}
	if _, err := h.recordCheckpoint(early, "p3", 50.2, "Rui"); err != nil {
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
			response, err := h.request(http.MethodPost, "/cases/"+early+"/seal", map[string]any{
				"reviewer": fmt.Sprintf("Verifier-%d", index+1),
				"summary":  "full checkpoint package sealed",
			})
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
		case http.StatusConflict:
			conflicts++
		default:
			return fmt.Errorf("unexpected concurrent seal status %d: %s", item.response.Status, item.response.Raw)
		}
	}
	if successes != 1 || conflicts != 1 {
		return fmt.Errorf("concurrent seal outcomes: %d success, %d conflict", successes, conflicts)
	}

	read, err := h.request(http.MethodGet, "/cases/"+early, nil)
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
		return fmt.Errorf("sealed package state = %q", state)
	}
	sealedPoints, err := nestedObjectSlice(read.Body, "seal", "checkpoints")
	if err != nil {
		return err
	}
	if len(sealedPoints) != 3 {
		return fmt.Errorf("seal froze %d points, expected 3", len(sealedPoints))
	}
	sealedCount, err := nestedFloat(read.Body, "seal", "checkpoint_count")
	if err != nil {
		return err
	}
	if sealedCount != 3 {
		return fmt.Errorf("seal checkpoint_count = %v", sealedCount)
	}
	casePoints, err := nestedObjectSlice(read.Body, "case", "checkpoints")
	if err != nil {
		return err
	}
	caseByCode := make(map[string]map[string]any, len(casePoints))
	for _, point := range casePoints {
		code, _ := point["code"].(string)
		caseByCode[code] = point
	}
	for _, sealed := range sealedPoints {
		code, _ := sealed["code"].(string)
		source, exists := caseByCode[code]
		if !exists {
			return fmt.Errorf("seal contains unknown checkpoint %q", code)
		}
		if sealed["measurement_id"] != source["measurement_id"] {
			return fmt.Errorf("sealed %s cites %v but case cites %v", code, sealed["measurement_id"], source["measurement_id"])
		}
		if sealed["attempts"] != source["attempts"] {
			return fmt.Errorf("sealed %s attempts do not match case", code)
		}
		if sealed["technician"] != source["technician"] {
			return fmt.Errorf("sealed %s technician does not match case", code)
		}
		if sealed["nominal"] != source["nominal"] || sealed["tolerance"] != source["tolerance"] {
			return fmt.Errorf("sealed %s plan requirements diverged from the case", code)
		}
	}

	mutation, err := h.recordCheckpoint(early, "p1", 10.01, "Rui")
	if err != nil {
		return err
	}
	if err := expectStatus(mutation, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(mutation, "state_conflict"); err != nil {
		return err
	}
	reopen, err := h.request(http.MethodPost, "/cases/"+early+"/reopen", map[string]any{
		"actor":  "Lin",
		"reason": "sealed packages never reopen",
	})
	if err != nil {
		return err
	}
	if err := expectStatus(reopen, http.StatusConflict); err != nil {
		return err
	}
	if err := expectErrorCode(reopen, "state_conflict"); err != nil {
		return err
	}

	events, err := h.request(http.MethodGet, "/cases/"+early+"/events", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(events, http.StatusOK); err != nil {
		return err
	}
	if passEvents := countEvents(events.Body, "checkpoint_passed"); passEvents != 3 {
		return fmt.Errorf("audit trail has %d checkpoint_passed events, expected 3", passEvents)
	}
	if countEvents(events.Body, "case_sealed") != 1 {
		return errors.New("audit trail is missing the single case_sealed event")
	}
	return nil
}

func countEvents(root map[string]any, kind string) int {
	items, err := nestedSlice(root, "events")
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

func (h *harness) openCasePlan(deviceID string, checkpoints []map[string]any) (apiResponse, error) {
	points := make([]any, 0, len(checkpoints))
	for _, point := range checkpoints {
		points = append(points, point)
	}
	response, err := h.request(http.MethodPost, "/cases", map[string]any{
		"device_id":   deviceID,
		"steward":     "Qiao",
		"checkpoints": points,
	})
	if err != nil {
		return apiResponse{}, err
	}
	return response, nil
}

func (h *harness) openThreePointCase(tag string, _ *harness) (string, error) {
	deviceID, err := h.createDevice(tag)
	if err != nil {
		return "", err
	}
	opened, err := h.openCasePlan(deviceID, []map[string]any{
		{"code": "p1", "label": "low range", "nominal": 10.0, "tolerance": 0.1},
		{"code": "p2", "label": "mid range", "nominal": 25.0, "tolerance": 0.5},
		{"code": "p3", "label": "high range", "nominal": 50.0, "tolerance": 1.0},
	})
	if err != nil {
		return "", err
	}
	if err := expectStatus(opened, http.StatusCreated); err != nil {
		return "", err
	}
	return nestedString(opened.Body, "case", "id")
}

func (h *harness) recordCheckpoint(caseID, code string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/checkpoints", map[string]any{
		"code":       code,
		"observed":   observed,
		"technician": technician,
	})
}

func (h *harness) recordMeasurement(caseID string, observed float64, technician string) (apiResponse, error) {
	return h.request(http.MethodPost, "/cases/"+caseID+"/measurements", map[string]any{
		"observed":   observed,
		"technician": technician,
	})
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

func nestedPathString(root map[string]any, path ...any) (string, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%v is not a string", path)
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

func nestedObjectSlice(root map[string]any, path ...any) ([]map[string]any, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return nil, err
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%v is not an array", path)
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%v contains a non-object element", path)
		}
		result = append(result, object)
	}
	return result, nil
}

func nestedFloat(root map[string]any, path ...any) (float64, error) {
	value, err := nestedAny(root, path...)
	if err != nil {
		return 0, err
	}
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("%v is not a number", path)
	}
	return number, nil
}

func checkpointByCode(root map[string]any, code string) (map[string]any, error) {
	points, err := nestedObjectSlice(root, "case", "checkpoints")
	if err != nil {
		return nil, err
	}
	for _, point := range points {
		if point["code"] == code {
			return point, nil
		}
	}
	return nil, fmt.Errorf("checkpoint %q is missing", code)
}

func checkpointFloat(root map[string]any, code, field string) (float64, error) {
	point, err := checkpointByCode(root, code)
	if err != nil {
		return 0, err
	}
	value, ok := point[field].(float64)
	if !ok {
		return 0, fmt.Errorf("checkpoint %s field %s is not a number", code, field)
	}
	return value, nil
}

func latestMeasurement(root map[string]any, measurementID string) (map[string]any, error) {
	measurements, err := nestedSlice(root, "measurements")
	if err != nil {
		return nil, err
	}
	for _, item := range measurements {
		measurement, ok := item.(map[string]any)
		if ok && measurement["id"] == measurementID {
			return measurement, nil
		}
	}
	return nil, fmt.Errorf("measurement %s is missing", measurementID)
}

func measurementExists(items []any, measurementID string) bool {
	for _, item := range items {
		measurement, ok := item.(map[string]any)
		if ok && measurement["id"] == measurementID {
			return true
		}
	}
	return false
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
