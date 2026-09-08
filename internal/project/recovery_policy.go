package project

import (
	"errors"
	"fmt"
)

type RecoveryPolicy string

const (
	RecoveryPolicyApprovalRequired RecoveryPolicy = "approval_required"
	RecoveryPolicyAutomatic        RecoveryPolicy = "automatic"
)

var ErrInvalidRecoveryPolicy = errors.New("invalid project recovery policy")

func (policy RecoveryPolicy) IsValid() bool {
	return policy == RecoveryPolicyApprovalRequired ||
		policy == RecoveryPolicyAutomatic
}

func NormalizeRecoveryPolicy(policy RecoveryPolicy) (RecoveryPolicy, error) {
	if policy == "" {
		return RecoveryPolicyApprovalRequired, nil
	}
	if !policy.IsValid() {
		return "", fmt.Errorf("%w: %q", ErrInvalidRecoveryPolicy, policy)
	}
	return policy, nil
}
