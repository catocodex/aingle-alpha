package tangle

import (
	"sync"

	"github.com/pkg/errors"

	"github.com/Ariwonto/aingle-alpha/pkg/model/aingle"
	"github.com/Ariwonto/aingle-alpha/pkg/model/milestone"
)

var (
	solidEntryPoints     *aingle.SolidEntryPoints
	solidEntryPointsLock sync.RWMutex

	ErrSolidEntryPointsAlreadyInitialized = errors.New("solidEntryPoints already initialized")
	ErrSolidEntryPointsNotInitialized     = errors.New("solidEntryPoints not initialized")
)

func ReadLockSolidEntryPoints() {
	solidEntryPointsLock.RLock()
}

func ReadUnlockSolidEntryPoints() {
	solidEntryPointsLock.RUnlock()
}

func WriteLockSolidEntryPoints() {
	solidEntryPointsLock.Lock()
}

func WriteUnlockSolidEntryPoints() {
	solidEntryPointsLock.Unlock()
}

func loadSolidEntryPoints() {
	WriteLockSolidEntryPoints()
	defer WriteUnlockSolidEntryPoints()

	if solidEntryPoints != nil {
		panic(ErrSolidEntryPointsAlreadyInitialized)
	}

	points, err := readSolidEntryPoints()
	if points != nil && err == nil {
		solidEntryPoints = points
	} else {
		solidEntryPoints = aingle.NewSolidEntryPoints()
	}
}

func SolidEntryPointsContain(txHash aingle.Hash) bool {
	ReadLockSolidEntryPoints()
	defer ReadUnlockSolidEntryPoints()

	if solidEntryPoints == nil {
		panic(ErrSolidEntryPointsNotInitialized)
	}
	return solidEntryPoints.Contains(txHash)
}

func SolidEntryPointsIndex(txHash aingle.Hash) (milestone.Index, bool) {
	ReadLockSolidEntryPoints()
	defer ReadUnlockSolidEntryPoints()

	if solidEntryPoints == nil {
		panic(ErrSolidEntryPointsNotInitialized)
	}
	return solidEntryPoints.Index(txHash)
}

// WriteLockSolidEntryPoints must be held while entering this function
func SolidEntryPointsAdd(txHash aingle.Hash, milestoneIndex milestone.Index) {
	if solidEntryPoints == nil {
		panic(ErrSolidEntryPointsNotInitialized)
	}
	solidEntryPoints.Add(txHash, milestoneIndex)
}

// WriteLockSolidEntryPoints must be held while entering this function
func ResetSolidEntryPoints() {
	if solidEntryPoints == nil {
		panic(ErrSolidEntryPointsNotInitialized)
	}
	solidEntryPoints.Clear()
}

// WriteLockSolidEntryPoints must be held while entering this function
func StoreSolidEntryPoints() {
	if solidEntryPoints == nil {
		panic(ErrSolidEntryPointsNotInitialized)
	}
	storeSolidEntryPoints(solidEntryPoints)
}
