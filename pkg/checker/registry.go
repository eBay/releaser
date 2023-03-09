package checker

import (
	"fmt"
	"sync"
	"time"
)

// Interface defines the interface for a check.
type Interface interface {
	Check(data []byte, lastRun time.Time) error
}

// lock ensure safety for accessing factories.
var lock sync.Mutex

// checks are all the registered factories.
var checks = map[string]Interface{}

// Register registers one implementation of check provider.
func Register(key string, impl Interface) error {
	lock.Lock()
	defer lock.Unlock()

	if _, found := checks[key]; found {
		return fmt.Errorf("%s is already registered", key)
	}

	checks[key] = impl
	return nil
}

// HasCheck returns true if there is a check factory registered for this key.
func HasCheck(key string) bool {
	lock.Lock()
	defer lock.Unlock()

	return checks[key] != nil
}

// GetCheck returns a check instance for the specified key.
func GetCheck(key string) (Interface, error) {
	lock.Lock()
	defer lock.Unlock()

	impl, found := checks[key]
	if !found {
		return nil, fmt.Errorf("no check factory registered for %s", key)
	}

	return impl, nil
}
