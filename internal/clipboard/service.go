package clipboard

import (
	"sync"
)

type Service struct {
	mu       sync.RWMutex
	contents map[string]string // session_id -> clipboard content
}

func NewService() *Service {
	return &Service{
		contents: make(map[string]string),
	}
}

func (s *Service) Push(sessionID, content string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contents[sessionID] = content
}

func (s *Service) Pull(sessionID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contents[sessionID]
}
