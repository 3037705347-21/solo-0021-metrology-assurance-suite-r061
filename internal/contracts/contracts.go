package contracts

import "example.com/solo-0021-metrology-assurance-suite/internal/domain"

type RegisterDeviceRequest struct {
	AssetTag string `json:"asset_tag"`
	Model    string `json:"model"`
	Room     string `json:"room"`
	Steward  string `json:"steward"`
}

// CheckpointInput defines one point of the measurement plan at case opening.
type CheckpointInput struct {
	Code      string  `json:"code"`
	Label     string  `json:"label"`
	Nominal   float64 `json:"nominal"`
	Tolerance float64 `json:"tolerance"`
}

type OpenCaseRequest struct {
	DeviceID string `json:"device_id"`
	Steward  string `json:"steward"`
	// Nominal/Tolerance keep the single-point legacy contract. They apply only
	// when Checkpoints is absent; both forms cannot be mixed.
	Nominal     float64            `json:"nominal"`
	Tolerance   float64            `json:"tolerance"`
	Checkpoints *[]CheckpointInput `json:"checkpoints,omitempty"`
}

type RecordMeasurementRequest struct {
	Observed   float64 `json:"observed"`
	Technician string  `json:"technician"`
}

type RecordCheckpointRequest struct {
	Code       string  `json:"code"`
	Observed   float64 `json:"observed"`
	Technician string  `json:"technician"`
}

type ReopenCaseRequest struct {
	Actor  string `json:"actor"`
	Reason string `json:"reason"`
}

type SealCaseRequest struct {
	Reviewer string `json:"reviewer"`
	Summary  string `json:"summary"`
}

type DeviceResponse struct {
	Device domain.Device `json:"device"`
}

type CaseResponse struct {
	Case         domain.VerificationCase `json:"case"`
	Measurements []domain.Measurement    `json:"measurements"`
	Seal         *domain.EvidenceSeal    `json:"seal,omitempty"`
	Events       []domain.AuditEvent     `json:"events"`
}

type EventsResponse struct {
	Events []domain.AuditEvent `json:"events"`
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
