package datagen

import (
	"testing"
	"time"
)

func TestBurstPhasesAlternateAndAverageToTargetRate(t *testing.T) {
	t.Parallel()
	const targetRate = 10
	quiet := phaseRate(targetRate, phaseAt(0, time.Second))
	spike := phaseRate(targetRate, phaseAt(time.Second, time.Second))
	if quiet != 4 {
		t.Fatalf("quiet rate = %v, want 4", quiet)
	}
	if spike != 16 {
		t.Fatalf("spike rate = %v, want 16", spike)
	}
	if (quiet+spike)/2 != targetRate {
		t.Fatalf("average rate = %v, want %d", (quiet+spike)/2, targetRate)
	}
}

func TestPhaseIntervalSlowsQuietAndAcceleratesSpike(t *testing.T) {
	t.Parallel()
	quiet := phaseInterval(10, burstQuiet)
	spike := phaseInterval(10, burstSpike)
	if quiet != 250*time.Millisecond {
		t.Fatalf("quiet interval = %s", quiet)
	}
	if spike != 62500*time.Microsecond {
		t.Fatalf("spike interval = %s", spike)
	}
}
