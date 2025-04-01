package cache

import (
	"github.com/bugfixes/go-bugfixes/logs"
	"github.com/flags-gg/go-flags/flag"
	"sync"
	"time"
)

type Memory struct {
	Flags       sync.Map
	cacheTTL    int64
	nextRefresh int64
	mu          sync.Mutex
}

func (m *Memory) Get(name string) (bool, bool) {
	value, ok := m.Flags.Load(name)
	if !ok {
		return false, false
	}
	featureFlag, ok := value.(flag.FeatureFlag)
	if !ok {
		return false, false
	}
	return featureFlag.Enabled, true
}

func (m *Memory) GetAll() ([]flag.FeatureFlag, error) {
	var allFlags []flag.FeatureFlag
	m.Flags.Range(func(key, value interface{}) bool {
		allFlags = append(allFlags, flag.FeatureFlag{
			Enabled: value.(bool),
			Details: flag.Details{
				Name: key.(string),
			},
		})
		return true
	})

	return allFlags, nil
}

func (m *Memory) Refresh(flags []flag.FeatureFlag, intervalAllowed int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Flags = sync.Map{}
	for _, f := range flags {
		m.Flags.Store(f.Details.Name, f)
	}
	m.cacheTTL = int64(intervalAllowed)
	m.nextRefresh = time.Now().Add(time.Duration(m.cacheTTL) * time.Second).Unix()

	return nil
}

func (m *Memory) ShouldRefreshCache() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Now().Unix() > m.nextRefresh
}

func (m *Memory) Init() error {
	m.cacheTTL = 60
	m.nextRefresh = time.Now().Add(time.Duration(-90) * time.Second).Unix()
	return nil
}

func NewMemory() *Memory {
	m := Memory{}

	if err := m.Init(); err != nil {
		logs.Fatalf("failed to initialize memory cache: %v", err)
	}

	return &m
}
