package worker

import "net/http"

const (
	outcomeRetirementContractVersion = "engram.outcome-retirement.v1"
	outcomeRetirementCode            = "OUTCOME_CALLBACK_RETIRED"
	outcomeRetirementAction          = "upgrade_outcome_adapter"
)

type outcomeRetirementResponse struct {
	ContractVersion string `json:"contract_version"`
	Code            string `json:"code"`
	Action          string `json:"action"`
}

func (s *Service) handleOutcomeCallbackRetirement(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusGone)
	writeJSON(w, outcomeRetirementResponse{
		ContractVersion: outcomeRetirementContractVersion,
		Code:            outcomeRetirementCode,
		Action:          outcomeRetirementAction,
	})
}
