package testenv

import "time"

// macRunnerDefaultStopGrace is the Docker stop period of a tests service that
// does not set stop_grace_period.
const macRunnerDefaultStopGrace = 10 * time.Second

// stopGrace returns the stop_grace_period of the tests service.
func (prepared *macRunnerPreparation) stopGrace() time.Duration {
	if prepared.service.StopGracePeriod == nil {
		return macRunnerDefaultStopGrace
	}

	return time.Duration(*prepared.service.StopGracePeriod)
}

// stopTimeout returns the container stop timeout in whole seconds, or nil when
// the tests service does not set stop_grace_period.
func (prepared *macRunnerPreparation) stopTimeout() *int {
	if prepared.service.StopGracePeriod == nil {
		return nil
	}

	seconds := int(prepared.stopGrace() / time.Second)

	return &seconds
}

// macRunnerSameStopTimeout reports whether a runner has the requested stop timeout.
func macRunnerSameStopTimeout(actual, requested *int) bool {
	if requested == nil {
		return actual == nil
	}

	return actual != nil && *actual == *requested
}
