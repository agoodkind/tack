// backup_staleness_cause.go names why a backup mechanism's age is unknown. An
// unknown age is always stale, but it covers two situations that support
// different claims: a mechanism that never recorded a success has produced
// nothing, while one whose record could not be read may be working. The alarm
// words key on this value rather than on the detail text.

package ops

// backupStalenessUnknownCause is the kind of unknown an age-unknown metric
// carries.
type backupStalenessUnknownCause int

const (
	// backupStalenessAgeKnown is the zero value: the age is known and no
	// cause applies.
	backupStalenessAgeKnown backupStalenessUnknownCause = iota
	// backupStalenessNeverRecorded means the store holds no success at all:
	// no marker, no complete export run, no restorable point.
	backupStalenessNeverRecorded
	// backupStalenessUnreadable means a reading could not be taken or
	// trusted: the store, marker, or status could not be read, or the
	// timestamp it carried could not be dated.
	backupStalenessUnreadable
)
