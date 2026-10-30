package computeruse

import (
	"errors"
	"fmt"
)

// ErrorCode 保持 OpenHanako 的业务错误码，方便 Agent 判断是否应刷新或停止。
type ErrorCode string

const (
	ErrDisabled              ErrorCode = "COMPUTER_USE_DISABLED"
	ErrRequiresVisionModel   ErrorCode = "COMPUTER_USE_REQUIRES_VISION_MODEL"
	ErrOSPermissionDenied    ErrorCode = "OS_PERMISSION_DENIED"
	ErrAppApprovalRequired   ErrorCode = "APP_APPROVAL_REQUIRED"
	ErrLeaseNotFound         ErrorCode = "LEASE_NOT_FOUND"
	ErrLeaseReleased         ErrorCode = "LEASE_RELEASED"
	ErrStaleSnapshot         ErrorCode = "STALE_SNAPSHOT"
	ErrTargetNotFound        ErrorCode = "TARGET_NOT_FOUND"
	ErrCapabilityUnsupported ErrorCode = "CAPABILITY_UNSUPPORTED"
	ErrActionBlocked         ErrorCode = "ACTION_BLOCKED_BY_POLICY"
	ErrRequiresForeground    ErrorCode = "ACTION_REQUIRES_FOREGROUND"
	ErrInputApprovalRequired ErrorCode = "ACTION_REQUIRES_INPUT_INJECTION_APPROVAL"
	ErrProviderUnavailable   ErrorCode = "PROVIDER_UNAVAILABLE"
	ErrProviderCrashed       ErrorCode = "PROVIDER_CRASHED"
	ErrInvalidArgument       ErrorCode = "INVALID_ARGUMENT"
)

type ComputerUseError struct {
	Code    ErrorCode      `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
	Cause   error          `json:"-"`
}

func (e *ComputerUseError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }
func (e *ComputerUseError) Unwrap() error { return e.Cause }
func failure(code ErrorCode, message string, details ...map[string]any) *ComputerUseError {
	e := &ComputerUseError{Code: code, Message: message}
	if len(details) > 0 {
		e.Details = details[0]
	}
	return e
}
func ErrorHasCode(err error, code ErrorCode) bool {
	var e *ComputerUseError
	return errors.As(err, &e) && e.Code == code
}
func errorResult(err error) *ToolResult {
	var e *ComputerUseError
	if errors.As(err, &e) {
		return &ToolResult{Error: e.Message, ErrorCode: e.Code, Details: e.Details}
	}
	return &ToolResult{Error: err.Error(), ErrorCode: ErrProviderCrashed}
}
