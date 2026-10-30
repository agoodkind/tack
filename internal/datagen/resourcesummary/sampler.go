package resourcesummary

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type samplerLine struct {
	Time     time.Time      `json:"time"`
	Error    string         `json:"error"`
	Meminfo  samplerMeminfo `json:"meminfo_kib"`
	Pressure string         `json:"pressure"`
	CPUDisk  samplerCPUDisk `json:"cpu_disk"`
}

type samplerMeminfo struct {
	MemAvailable int64 `json:"MemAvailable"`
}

// samplerCPUDisk keys guests and guest pressure by guest ID.
type samplerCPUDisk struct {
	Host     samplerHost                     `json:"host"`
	Guests   map[string]samplerGuest         `json:"guests"`
	Pressure map[string]samplerGuestPressure `json:"pressure"`
}

type samplerHost struct {
	CPUPressure []string `json:"/proc/pressure/cpu"`
	IOPressure  []string `json:"/proc/pressure/io"`
	LoadAverage []string `json:"/proc/loadavg"`
}

type samplerGuest struct {
	CPU samplerGuestCPU `json:"cpu"`
	IO  []string        `json:"io"`
}

type samplerGuestCPU struct {
	// UsageUsec accepts a JSON number or a JSON string that contains a number.
	UsageUsec json.Number `json:"usage_usec"`
}

type samplerGuestPressure struct {
	CPU []string `json:"cpu.pressure"`
	IO  []string `json:"io.pressure"`
}

// hostSample is one sampler line after parsing, with the readings of one guest.
type hostSample struct {
	time                   time.Time
	memAvailableGiB        float64
	memoryPressure         pressureReading
	cpuPressure            pressureReading
	ioPressure             pressureReading
	loadOneMinute          float64
	guestUsageMicroseconds int64
	guestIOStat            ioBytes
	guestCPUPressure       pressureReading
	guestIOPressure        pressureReading
}

// parseSample checks and parses the readings that the summary uses from one sampler line.
func parseSample(line samplerLine, guestID string) (hostSample, error) {
	guest, guestFound := line.CPUDisk.Guests[guestID]
	guestPressure, pressureFound := line.CPUDisk.Pressure[guestID]
	if !guestFound || !pressureFound {
		return hostSample{}, fmt.Errorf("sample %s has no guest %s", line.Time, guestID)
	}
	usage, err := guest.CPU.UsageUsec.Int64()
	if err != nil {
		return hostSample{}, wrapError("parse usage_usec at "+line.Time.String(), err)
	}
	ioStat, err := parseIOStat(guest.IO)
	if err != nil {
		return hostSample{}, err
	}
	load, err := parseLoad(line.CPUDisk.Host.LoadAverage)
	if err != nil {
		return hostSample{}, err
	}
	var sample hostSample
	sample.time = line.Time
	sample.memAvailableGiB = float64(line.Meminfo.MemAvailable) / kibibytesPerGibibyte
	sample.loadOneMinute = load
	sample.guestUsageMicroseconds = usage
	sample.guestIOStat = ioStat
	pressureSources := []struct {
		lines   []string
		reading *pressureReading
	}{
		{lines: strings.Split(line.Pressure, "\n"), reading: &sample.memoryPressure},
		{lines: line.CPUDisk.Host.CPUPressure, reading: &sample.cpuPressure},
		{lines: line.CPUDisk.Host.IOPressure, reading: &sample.ioPressure},
		{lines: guestPressure.CPU, reading: &sample.guestCPUPressure},
		{lines: guestPressure.IO, reading: &sample.guestIOPressure},
	}
	for _, source := range pressureSources {
		reading, pressureErr := parsePressure(source.lines)
		if pressureErr != nil {
			return hostSample{}, pressureErr
		}
		*source.reading = reading
	}
	return sample, nil
}
