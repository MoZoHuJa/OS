package reservation

import (
	"fmt"
	"syscall"
)

// ResourceLock serializes resource selection and reservation commits across
// goroutines and processes. The lock deliberately reuses the reservation
// store's stable flock file so no second lock-order can deadlock Store.Update.
type ResourceLock struct {
	store *Store
}

func NewResourceLock(store *Store) (*ResourceLock, error) {
	if store == nil {
		return nil, fmt.Errorf("reservation: resource lock requires a store")
	}
	return &ResourceLock{store: store}, nil
}

// Lock returns an unlock function. Callers must defer it immediately.
func (l *ResourceLock) Lock() (func(), error) {
	if l == nil || l.store == nil {
		return nil, fmt.Errorf("reservation: resource lock is not initialized")
	}
	f, err := l.store.acquireFlock()
	if err != nil {
		return nil, err
	}
	unlock := func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}
	return unlock, nil
}
