package provisioning

import "time"

// The CSV parser and the row validator are reached directly, so a value's
// round trip and the parser's allocation can be asserted without a service.
var (
	SplitCSVLine   = splitCSVLine
	FormatCSVRow   = formatCSVRow
	ValidateScalar = validateScalar
)

// SetClock replaces the clock the cluster measurement ages by.
func SetClock(s *Service, now func() time.Time) { s.now = now }

// MaxCopyReads is how many participants' copies the schema reader reads at once.
const MaxCopyReads = maxCopyReads
