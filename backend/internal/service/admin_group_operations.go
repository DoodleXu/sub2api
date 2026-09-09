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
	return ValidateSimpleModeGroupOperation(s.cfg, operation)
}

func (s *adminServiceImpl) validateSimpleModeGroupAccess(group *Group) error {
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple && !IsGroupBindableInSimpleMode(group) {
		return infraerrors.BadRequest("SIMPLE_MODE_GROUP_NOT_BINDABLE", "composite groups are not supported in simple mode")
	}
	return nil
}
