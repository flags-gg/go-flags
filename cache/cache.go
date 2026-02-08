package cache

import (
	"github.com/flags-gg/go-flags/flag"
)

type Caching interface {
	Get(name string) (bool, bool)
	GetAll() ([]flag.FeatureFlag, error)
	Refresh(flags []flag.FeatureFlag, intervalAllowed int) error
	ShouldRefreshCache() bool
	Init() error
}

type Cache struct {
	Caching
}

type System struct {
	FileName *string
	IsMemory bool

	CacheSystem Caching
}

func NewSystem() *System {
	return &System{}
}

func (s *System) SetFileName(fileName *string) {
	s.FileName = fileName
}

func (s *System) NewMemory() {
	s.IsMemory = true
	s.CacheSystem = NewMemory()
}

func (s *System) NewSQLLite() {
	s.CacheSystem = NewSQLLite(s.FileName)
}
