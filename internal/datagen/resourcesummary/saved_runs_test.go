package resourcesummary_test

import (
	"time"

	"goodkind.io/tack/internal/datagen/resourcesummary"
)

// hostWant is the set of host and guest values that TACK-544 cites for one window.
type hostWant struct {
	samples            int
	failedSamples      int
	first              time.Time
	last               time.Time
	memAvailableMinGiB float64
	memoryPressure     resourcesummary.Pressure
	cpuPressureSome    float64
	loadOneMinuteMax   float64
	ioPressure         resourcesummary.Pressure
	guestCPUCores      float64
	guestCPUPressure   resourcesummary.Pressure
	guestIOPressure    resourcesummary.Pressure
	guestReadMiB       float64
	guestWriteMiB      float64
}

// nodeWant is the set of OpenSearch node values that TACK-544 cites for one run.
type nodeWant struct {
	name                  string
	readings              int
	cpuPercentMax         int
	processCPUPercentMax  int
	heapUsedMaxBytes      int64
	heapUsedMaxPercent    float64
	memoryUsedMaxPercent  float64
	diskAvailableMinBytes int64
	diskTotalBytes        int64
}

type savedRun struct {
	name            string
	samplerFile     string
	runLogFile      string
	window          resourcesummary.Window
	host            hostWant
	workloadSamples int
	node            nodeWant
}

func savedRuns() []savedRun {
	return []savedRun{
		{
			name:        "rate 1000",
			samplerFile: "tack575-554f2181.jsonl",
			runLogFile:  "r1000-554f2181-run.log",
			window: resourcesummary.Window{
				Start: time.Date(2026, time.October, 30, 10, 21, 38, 0, time.UTC),
				End:   time.Date(2026, time.October, 30, 10, 42, 16, 0, time.UTC),
			},
			host: hostWant{
				samples:            210,
				failedSamples:      0,
				first:              time.Date(2026, time.October, 30, 10, 21, 43, 0, time.UTC),
				last:               time.Date(2026, time.October, 30, 10, 42, 15, 0, time.UTC),
				memAvailableMinGiB: 4.874,
				memoryPressure:     resourcesummary.Pressure{SomeAvg10Max: 11.14, FullAvg10Max: 4.17},
				cpuPressureSome:    60.86,
				loadOneMinuteMax:   61.15,
				ioPressure:         resourcesummary.Pressure{SomeAvg10Max: 44.05, FullAvg10Max: 16.90},
				guestCPUCores:      0.805,
				guestCPUPressure:   resourcesummary.Pressure{SomeAvg10Max: 89.21, FullAvg10Max: 59.29},
				guestIOPressure:    resourcesummary.Pressure{SomeAvg10Max: 13.16, FullAvg10Max: 11.02},
				guestReadMiB:       160.5,
				guestWriteMiB:      0,
			},
			workloadSamples: 473,
			node: nodeWant{
				name:                  "tack-search1",
				readings:              473,
				cpuPercentMax:         100,
				processCPUPercentMax:  100,
				heapUsedMaxBytes:      251671608,
				heapUsedMaxPercent:    7.81,
				memoryUsedMaxPercent:  86.46,
				diskAvailableMinBytes: 37953208320,
				diskTotalBytes:        42949672960,
			},
		},
		{
			name:        "rate 3000",
			samplerFile: "tack575-554f2181-r3000.jsonl",
			runLogFile:  "r3000-554f2181-run.log",
			window: resourcesummary.Window{
				Start: time.Date(2026, time.October, 30, 15, 5, 13, 0, time.UTC),
				End:   time.Date(2026, time.October, 30, 15, 29, 57, 0, time.UTC),
			},
			host: hostWant{
				samples:            241,
				failedSamples:      0,
				first:              time.Date(2026, time.October, 30, 15, 5, 14, 0, time.UTC),
				last:               time.Date(2026, time.October, 30, 15, 29, 52, 0, time.UTC),
				memAvailableMinGiB: 4.577,
				memoryPressure:     resourcesummary.Pressure{SomeAvg10Max: 6.21, FullAvg10Max: 1.97},
				cpuPressureSome:    59.23,
				loadOneMinuteMax:   56.42,
				ioPressure:         resourcesummary.Pressure{SomeAvg10Max: 55.32, FullAvg10Max: 20.72},
				guestCPUCores:      0.732,
				guestCPUPressure:   resourcesummary.Pressure{SomeAvg10Max: 87.97, FullAvg10Max: 59.25},
				guestIOPressure:    resourcesummary.Pressure{SomeAvg10Max: 15.61, FullAvg10Max: 9.32},
				guestReadMiB:       259.8,
				guestWriteMiB:      0,
			},
			workloadSamples: 612,
			node: nodeWant{
				name:                  "tack-search1",
				readings:              612,
				cpuPercentMax:         100,
				processCPUPercentMax:  100,
				heapUsedMaxBytes:      244477312,
				heapUsedMaxPercent:    7.59,
				memoryUsedMaxPercent:  87.44,
				diskAvailableMinBytes: 37945737216,
				diskTotalBytes:        42949672960,
			},
		},
	}
}
