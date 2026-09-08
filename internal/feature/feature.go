package feature

import "time"

type State string

const StateDraft State = "draft"

type Feature struct {
	ID          string
	ProjectID   string
	Title       string
	Description string
	State       State
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
