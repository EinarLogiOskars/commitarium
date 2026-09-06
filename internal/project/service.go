package project

import (
	"crypto/rand"
	"errors"
	"strings"
	"time"
)

var ErrNameRequired = errors.New("project name is required")

type Service struct {
	generateID func() string
	now        func() time.Time
}

func NewService() *Service {
	return &Service{
		generateID: func() string {
			return "prj_" + rand.Text()
		},
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (s *Service) Create(name string) (Project, error) {
	sanitizedName := strings.TrimSpace(name)

	if sanitizedName == "" {
		return Project{}, ErrNameRequired
	}

	return Project{
		ID:        s.generateID(),
		Name:      sanitizedName,
		CreatedAt: s.now(),
	}, nil

}
