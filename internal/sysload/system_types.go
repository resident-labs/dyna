package sysload

import "time"

// Load is one reading. CPUPercent and Split are nil until two samples exist.
// Split values are percent of the whole machine. Memory values are bytes.
type Load struct {
	At         time.Time  `json:"at"`
	CPUs       int        `json:"cpus"`
	Load       [3]float64 `json:"load" tstype:"LoadAverage"`
	CPUPercent *float64   `json:"cpuPercent" tstype:"number | null,required"`
	Split      *Split     `json:"split" tstype:"Split | null,required"`
	Memory     Memory     `json:"memory"`
	Top        []Top      `json:"top" tstype:"Top[] | null"`
	DynaKnown  bool       `json:"dynaKnown"`
	// Groups feed the doctor's stored samples; the web app does not read them.
	Groups []Group `json:"-"`
}

// Split divides the busy CPU time of the machine.
type Split struct {
	Engine    float64 `json:"engine"`
	Agents    float64 `json:"agents"`
	Computers float64 `json:"computers"`
	Other     float64 `json:"other"`
}

// Memory holds machine memory in bytes. Dyna is zero when DynaKnown is false.
type Memory struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
	Dyna  uint64 `json:"dyna"`
}

// Top is one session (or run) and the cores it used in the last interval.
type Top struct {
	ID    string  `json:"id"`
	Cores float64 `json:"cores"`
}
