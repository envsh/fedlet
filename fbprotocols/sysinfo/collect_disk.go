// collect_disk.go — mounted filesystem capacity and inode usage.
//
// Partitions are enumerated with all=false so gopsutil applies its own
// pseudo-filesystem filter (proc, sysfs, tmpfs and friends are dropped); the
// result is then deduplicated by mountpoint because bind mounts otherwise
// report the same device twice.
//
// A partition whose usage cannot be read is still published with -1 fields
// (rule D) so the mount is visible; only a failed enumeration yields an empty
// array (rule E).
package sysinfo

import (
	"github.com/shirou/gopsutil/v4/disk"
)

// invalidDiskInfo returns a DiskInfo whose every numeric field is the -1
// sentinel, ready to be filled in or published as a failed item.
func invalidDiskInfo(p disk.PartitionStat) DiskInfo {
	return DiskInfo{
		Device:     p.Device,
		Mountpoint: p.Mountpoint,
		Fstype:     p.Fstype,
		Opts:       p.Opts,
		Total:      InvalidNum, Free: InvalidNum, Used: InvalidNum,
		UsedPercent:       InvalidNum,
		InodesTotal:       InvalidNum,
		InodesFree:        InvalidNum,
		InodesUsed:        InvalidNum,
		InodesUsedPercent: InvalidNum,
	}
}

// collectDisk fills Snapshot.Disk.
func collectDisk(s *Snapshot, errs *errCollector) {
	parts, err := disk.Partitions(false)
	if err != nil {
		errs.Add("disk", err.Error())
		return
	}
	if len(parts) == 0 {
		// Rule E: the platform reported nothing at all.
		errs.Add("disk", "no partitions reported")
		return
	}

	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		if p.Mountpoint == "" || seen[p.Mountpoint] {
			continue
		}
		seen[p.Mountpoint] = true

		d := invalidDiskInfo(p)
		u, uerr := disk.Usage(p.Mountpoint)
		if uerr != nil {
			// Rule D: this mount failed, its siblings are unaffected.
			errs.Add("disk:"+p.Mountpoint, uerr.Error())
			s.Disk = append(s.Disk, d)
			continue
		}
		// A zero total means statfs returned no size (permission, broken
		// mount); guardPct turns the resulting ratio into -1 as well.
		d.Total = guardNum(float64(u.Total))
		d.Free = guardNum(float64(u.Free))
		d.Used = guardNum(float64(u.Used))
		d.UsedPercent = guardPct(float64(u.Used), float64(u.Total))
		d.InodesTotal = guardNum(float64(u.InodesTotal))
		d.InodesFree = guardNum(float64(u.InodesFree))
		d.InodesUsed = guardNum(float64(u.InodesUsed))
		d.InodesUsedPercent = guardPct(float64(u.InodesUsed), float64(u.InodesTotal))
		s.Disk = append(s.Disk, d)
	}

	if len(s.Disk) == 0 {
		errs.Add("disk", "no usable partitions")
	}
}
