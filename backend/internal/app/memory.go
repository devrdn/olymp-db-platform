package app

import (
	"log/slog"
	"math"
	"runtime/debug"

	"github.com/devrdn/db-contest/backend/internal/platform/password"
)

// baseMemory is the estimated memory need besides password hashing, the
// figure the deployment's GOMEMLIMIT arithmetic starts from.
const baseMemory int64 = 1 << 30

// checkMemoryLimit warns when the runtime's memory limit, if one is set, is
// below what the configured hashing concurrency needs on top of baseMemory.
func checkMemoryLimit(log *slog.Logger, concurrency int) {
	// A negative argument reads the current limit without changing it.
	warnIfMemoryLimitTooLow(log, debug.SetMemoryLimit(-1), concurrency)
}

// warnIfMemoryLimitTooLow is checkMemoryLimit with the limit passed in. It
// warns rather than refuses, since GOMEMLIMIT is a soft target; a too-low
// limit makes the collector thrash during a burst of sign-ins.
func warnIfMemoryLimitTooLow(log *slog.Logger, limit int64, concurrency int) {
	if limit == math.MaxInt64 {
		return // GOMEMLIMIT is not set; there is nothing to compare against.
	}
	need := baseMemory + int64(concurrency)*password.MemoryPerHash
	if limit >= need {
		return
	}
	log.Warn("GOMEMLIMIT is below what password hashing needs at the configured concurrency",
		"gomemlimit_mib", limit>>20,
		"needed_mib", need>>20,
		"password_hash_concurrency", concurrency,
		"mib_per_hash", password.MemoryPerHash>>20,
		"base_mib", baseMemory>>20,
	)
}
