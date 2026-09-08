package project

import "time"

type Project struct {
	ID             string
	Name           string
	RecoveryPolicy RecoveryPolicy
	CreatedAt      time.Time
}
