package project

import "errors"

type MergePolicy string

const (
	MergePolicyRequireUserApproval MergePolicy = "require_user_approval"
	MergePolicyAutoAfterGates      MergePolicy = "auto_after_gates"
)

var ErrInvalidMergePolicy = errors.New("invalid project merge policy")

func DefaultMergePolicy() MergePolicy {
	return MergePolicyRequireUserApproval
}

func NormalizeMergePolicy(policy MergePolicy) (MergePolicy, error) {
	if policy == "" {
		return DefaultMergePolicy(), nil
	}
	switch policy {
	case MergePolicyRequireUserApproval, MergePolicyAutoAfterGates:
		return policy, nil
	default:
		return "", ErrInvalidMergePolicy
	}
}
