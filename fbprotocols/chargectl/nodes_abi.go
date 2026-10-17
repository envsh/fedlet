//go:build linux || android

package chargectl

// preferACC selects which vendor table is tried first. The ACC-derived table is
// the default because it covers far more Android vendors; the self-authored ABI
// table is the fallback. It is a var only so tests can exercise both orders.
var preferACC = true

// kernelCandidates are the standard kernel charge-control nodes. They are always
// probed first: when present they give the cleanest hysteresis and must win over
// vendor switches.
var kernelCandidates = []candidate{
	{
		label:    "charge_control threshold (kernel ABI)",
		mode:     modeThreshold,
		startPat: "*/charge_control_start_threshold",
		endPat:   "*/charge_control_end_threshold",
	},
	{
		label:  "charge_control_end_threshold only (kernel ABI)",
		mode:   modeLevel,
		endPat: "*/charge_control_end_threshold",
	},
}

// abiExtras are the remaining kernel nodes, tried after the vendor tables.
var abiExtras = []candidate{
	{
		label:  "charge_stop_level (kernel ABI)",
		mode:   modeLevel,
		endPat: "*/charge_stop_level",
	},
	{
		label:      "charge_behaviour (kernel ABI)",
		mode:       modeSwitch,
		pat:        "*/charge_behaviour",
		enableVal:  "auto",
		disableVal: "inhibit-charge",
	},
}

// laptopExtras are last-resort laptop toggles that impose a fixed limit and
// cannot be set to an arbitrary percentage. They are tried only after every
// percentage-aware node, so their coarser limit is used only when nothing
// better exists.
var laptopExtras = []candidate{
	{
		// IdeaPad "battery conservation mode": 1 limits the pack to roughly
		// 50-60%, 0 restores the normal charge-to-100% behaviour. Enable it to
		// stop, clear it to resume. Documented in sysfs-platform-ideapad-laptop.
		label:      "ideapad conservation_mode",
		mode:       modeSwitch,
		pat:        "/sys/bus/platform/devices/VPC2004:*/conservation_mode",
		enableVal:  "0",
		disableVal: "1",
	},
}

// orderedCandidates returns the probe order without mutating any table:
// kernel ABI first, then the preferred vendor table, then the rest, then the
// fixed-limit laptop toggles.
func orderedCandidates() []candidate {
	var vendor, extras []candidate
	if preferACC {
		vendor, extras = accCandidates, abiExtras
	} else {
		vendor, extras = abiExtras, accCandidates
	}
	out := make([]candidate, 0, len(kernelCandidates)+len(vendor)+len(extras)+len(laptopExtras))
	out = append(out, kernelCandidates...)
	out = append(out, vendor...)
	out = append(out, extras...)
	out = append(out, laptopExtras...)
	return out
}
