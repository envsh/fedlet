// types.go — wire schema for the sysinfo snapshot.
//
// Every numeric field uses -1 as the "value unavailable" sentinel; booleans are
// encoded as int tri-states (-1 unknown / 0 false / 1 true) because JSON bool
// cannot express "unknown"; strings use "" for unknown. Whole sections that
// cannot be produced (no battery, no thermal sensor) are emitted as empty
// arrays with a reason appended to Snapshot.Errors, never as -1 placeholder
// entries.
//
// Numeric fields are float64 throughout so a single guard applies everywhere;
// integral values still marshal without a fractional part (json.Marshal of
// float64(12345) yields 12345).
//
// The five invalid-value rules (A function-level, B field-level guard,
// C first-round delta, D single-item failure, E whole-section missing) are
// documented in README.md and implemented in collect.go.
package sysinfo

import "time"

// InvalidNum is written to any numeric field whose value could not be obtained
// this round. It is the only negative value permitted in the schema.
const InvalidNum = -1

// Tri-state encoding for boolean-valued fields: present, charging, ac_power.
const (
	TriUnknown = -1
	TriNo      = 0
	TriYes     = 1
)

// DefaultInterval is the default sampling period. Deliberately odd so it does
// not phase-lock with the 600s/120s hot-board pollers.
const DefaultInterval = 123 * time.Second

// Snapshot is one full sampling round, published as a single JSON document.
type Snapshot struct {
	Kind        string        `json:"kind"` // always "sysinfo"
	PublishedAt int64         `json:"published_at"`
	Host        HostInfo      `json:"host"`
	CPU         CPUInfo       `json:"cpu"`
	Mem         MemInfo       `json:"mem"`
	Load        LoadInfo      `json:"load"`
	Disk        []DiskInfo    `json:"disk"`
	IO          []IOInfo      `json:"io"`
	Net         []NetInfo     `json:"net"`
	Temp        []TempInfo    `json:"temp"`
	Battery     []BatteryInfo `json:"battery"`
	Errors      []string      `json:"errors"`
}

// HostInfo identifies the machine. String fields are "" when unknown.
type HostInfo struct {
	Hostname        string  `json:"hostname"`
	OS              string  `json:"os"`
	Platform        string  `json:"platform"`
	PlatformVersion string  `json:"platform_version"`
	Kernel          string  `json:"kernel"`
	Arch            string  `json:"arch"`
	Virtualization  string  `json:"virtualization"`
	HostID          string  `json:"host_id"`
	Procs           float64 `json:"procs"`
	UptimeSec       float64 `json:"uptime_sec"`
	BootTime        float64 `json:"boot_time"`
}

// CPUInfo reports utilisation and topology. Percent and PerCPU are -1 on the
// first round (rule C: no previous sample to diff against).
type CPUInfo struct {
	Percent        float64   `json:"percent"`
	PerCPU         []float64 `json:"per_cpu"`
	CountsLogical  float64   `json:"counts_logical"`
	CountsPhysical float64   `json:"counts_physical"`
	Model          string    `json:"model"`
	Mhz            float64   `json:"mhz"`
}

// MemInfo reports memory and swap in bytes.
type MemInfo struct {
	Total           float64 `json:"total"`
	Available       float64 `json:"available"`
	Used            float64 `json:"used"`
	Free            float64 `json:"free"`
	UsedPercent     float64 `json:"used_percent"`
	SwapTotal       float64 `json:"swap_total"`
	SwapUsed        float64 `json:"swap_used"`
	SwapUsedPercent float64 `json:"swap_used_percent"`
}

// LoadInfo reports the classic 1/5/15 minute load averages.
type LoadInfo struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

// DiskInfo is one mounted filesystem. Partitions are enumerated with
// gopsutil's all=false so pseudo filesystems (proc, sysfs, tmpfs...) are
// excluded.
type DiskInfo struct {
	Device            string   `json:"device"`
	Mountpoint        string   `json:"mountpoint"`
	Fstype            string   `json:"fstype"`
	Opts              []string `json:"opts"`
	Total             float64  `json:"total"`
	Free              float64  `json:"free"`
	Used              float64  `json:"used"`
	UsedPercent       float64  `json:"used_percent"`
	InodesTotal       float64  `json:"inodes_total"`
	InodesFree        float64  `json:"inodes_free"`
	InodesUsed        float64  `json:"inodes_used"`
	InodesUsedPercent float64  `json:"inodes_used_percent"`
}

// IOInfo is one block device. The cumulative counters come straight from the
// kernel; ReadSpeed/WriteSpeed are computed against the previous round and are
// -1 on the first round (rule C).
type IOInfo struct {
	Name       string  `json:"name"`
	ReadCount  float64 `json:"read_count"`
	WriteCount float64 `json:"write_count"`
	ReadBytes  float64 `json:"read_bytes"`
	WriteBytes float64 `json:"write_bytes"`
	ReadSpeed  float64 `json:"read_speed"`  // bytes/s
	WriteSpeed float64 `json:"write_speed"` // bytes/s
}

// NetInfo is one network interface with cumulative counters plus per-round
// rates (rule C on the first round).
type NetInfo struct {
	Name        string   `json:"name"`
	MAC         string   `json:"mac"`
	Flags       []string `json:"flags"` // gopsutil flags, e.g. ["up","broadcast","running"]
	BytesSent   float64  `json:"bytes_sent"`
	BytesRecv   float64  `json:"bytes_recv"`
	PacketsSent float64  `json:"packets_sent"`
	PacketsRecv float64  `json:"packets_recv"`
	ErrIn       float64  `json:"err_in"`
	ErrOut      float64  `json:"err_out"`
	DropIn      float64  `json:"drop_in"`
	DropOut     float64  `json:"drop_out"`
	SendSpeed   float64  `json:"send_speed"` // bytes/s
	RecvSpeed   float64  `json:"recv_speed"` // bytes/s
}

// TempInfo is one temperature sensor. Celsius/High/Critical are -1 when the
// kernel does not expose them.
type TempInfo struct {
	Sensor   string  `json:"sensor"`
	Celsius  float64 `json:"celsius"`
	High     float64 `json:"high"`
	Critical float64 `json:"critical"`
}

// BatteryInfo is one physical battery. Present/Charging/ACPower use the
// tri-state encoding; Capacity fields are mWh, ChargeRate mW, Voltage V and
// Temperature degrees Celsius (kernel 0.1C units already converted).
type BatteryInfo struct {
	Index         int     `json:"index"`
	Present       int     `json:"present"` // -1 unknown / 0 absent / 1 present
	Percent       float64 `json:"percent"`
	Charging      int     `json:"charging"` // -1 unknown / 0 not charging / 1 charging
	HealthPercent float64 `json:"health_percent"`
	Health        string  `json:"health"`      // "" when unknown
	Cycles        float64 `json:"cycles"`      // charge cycles, -1 when unsupported
	State         string  `json:"state"`       // Charging/Discharging/Full/Idle/... , "" unknown
	ACPower       int     `json:"ac_power"`    // -1 unknown / 0 on battery / 1 external
	Current       float64 `json:"current"`     // mWh
	Full          float64 `json:"full"`        // mWh
	Design        float64 `json:"design"`      // mWh
	ChargeRate    float64 `json:"charge_rate"` // mW, positive charging
	Voltage       float64 `json:"voltage"`     // V
	Temperature   float64 `json:"temperature"` // C
}
