package service

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// AdminGroupOperation identifies an administrative capability on groups.
// Keeping this policy separate lets handlers and services depend on the
// capability contract without coupling to any concrete group implementation.
type AdminGroupOperation string

const (
	AdminGroupOperationBasic          AdminGroupOperation = "basic"
	AdminGroupOperationDuplicate      AdminGroupOperation = "duplicate"
	AdminGroupOperationCompositeRoute AdminGroupOperation = "composite_route"
	AdminGroupOperationMultiplier     AdminGroupOperation = "multiplier"
	AdminGroupOperationRPMOverride    AdminGroupOperation = "rpm_override"
	AdminGroupOperationSort           AdminGroupOperation = "sort"
)

func ValidateSimpleModeGroupOperation(cfg *config.Config, operation AdminGroupOperation) error {
	if cfg != nil && cfg.RunMode == config.RunModeSimple && operation != AdminGroupOperationBasic {
		return infraerrors.New(http.StatusForbidden, "SIMPLE_MODE_OPERATION_UNSUPPORTED", "This operation is not supported in simple mode")
	}
	return nil
}

func (s *adminServiceImpl) ValidateSimpleModeGroupOperation(operation AdminGroupOperation) error {
	// Fork's aggregate admin service has no direct config dependency. Simple
	// mode handlers perform the mode gate at their boundary.
	return nil
}
