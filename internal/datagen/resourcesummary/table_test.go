package resourcesummary_test

import (
	"fmt"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/datagen/resourcesummary"
)

const (
	percentScale  = 100
	bytesPerGiB   = 1 << 30
	tableHeader   = "| Measurement | Value |\n|---|---|\n"
	nodeHeader    = "| Node | Readings | cpu_percent max | process_cpu_percent max | " +
		"heap_used max | heap fraction | memory used fraction max | disk_available min | " +
		"disk_total |\n|---|---|---|---|---|---|---|---|---|\n"
)

type tableRow struct {
	label string
	value string
}

func hostTable(host resourcesummary.HostSummary, guestID string) string {
	guest := "CT " + guestID
	rows := []tableRow{
		{label: "Hypervisor samples", value: strconv.Itoa(host.Samples)},
		{label: "Failed hypervisor samples", value: strconv.Itoa(host.FailedSamples)},
		{label: "First and last sample", value: fmt.Sprintf("%s to %s (%.0f s)",
			host.First.Format("15:04:05Z"), host.Last.Format("15:04:05Z"),
			host.Elapsed().Seconds())},
		{label: "Host MemAvailable min", value: fmt.Sprintf("%.3f GiB", host.MemAvailableMinGiB)},
		{label: "Host memory PSI some avg10 max", value: percent(host.MemoryPressure.SomeAvg10Max)},
		{label: "Host memory PSI full avg10 max", value: percent(host.MemoryPressure.FullAvg10Max)},
		{label: "Host CPU PSI some avg10 max", value: percent(host.CPUPressure.SomeAvg10Max)},
		{label: "Host 1-minute load max", value: fmt.Sprintf("%.2f", host.LoadOneMinuteMax)},
		{label: "Host IO PSI some avg10 max", value: percent(host.IOPressure.SomeAvg10Max)},
		{label: "Host IO PSI full avg10 max", value: percent(host.IOPressure.FullAvg10Max)},
		{label: guest + " average CPU", value: fmt.Sprintf("%.3f cores", host.GuestCPUCores)},
		{label: guest + " CPU PSI some avg10 max", value: percent(host.GuestCPUPressure.SomeAvg10Max)},
		{label: guest + " CPU PSI full avg10 max", value: percent(host.GuestCPUPressure.FullAvg10Max)},
		{label: guest + " IO PSI some avg10 max", value: percent(host.GuestIOPressure.SomeAvg10Max)},
		{label: guest + " IO PSI full avg10 max", value: percent(host.GuestIOPressure.FullAvg10Max)},
		{label: guest + " read", value: fmt.Sprintf("%.1f MiB", host.GuestReadMiB)},
		{label: guest + " write", value: fmt.Sprintf("%.1f MiB", host.GuestWriteMiB)},
	}
	var builder strings.Builder
	builder.WriteString(tableHeader)
	for _, row := range rows {
		fmt.Fprintf(&builder, "| %s | %s |\n", row.label, row.value)
	}
	return builder.String()
}

func nodeTable(workload resourcesummary.RunSummary) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Workload samples: %d\n", workload.Samples)
	builder.WriteString(nodeHeader)
	for _, node := range workload.Nodes {
		fmt.Fprintf(&builder, "| %s | %d | %d%% | %d%% | %s | %s | %s | %s | %s |\n",
			node.Name, node.Readings, node.CPUPercentMax, node.ProcessCPUPercentMax,
			gibibytes(node.HeapUsedMaxBytes), percent(node.HeapUsedMaxFraction*percentScale),
			percent(node.MemoryUsedMaxFraction*percentScale),
			gibibytes(node.DiskAvailableMinBytes), gibibytes(node.DiskTotalBytes))
	}
	return builder.String()
}

func percent(value float64) string {
	return fmt.Sprintf("%.2f%%", value)
}

func gibibytes(value int64) string {
	return fmt.Sprintf("%.3f GiB (%d bytes)", float64(value)/bytesPerGiB, value)
}
