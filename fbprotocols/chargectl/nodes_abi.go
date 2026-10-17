//go:build linux || android

package chargectl

// preferACC selects which table is tried first. The ACC-derived table is the
// default because it covers far more Android vendors; the self-authored ABI
// table is the fallback. It is a var only so tests can exercise both orders.
var preferACC = true

// orderedCandidates returns the probe order without mutating either table.
func orderedCandidates() []candidate {
	if preferACC {
		return append(append([]candidate{}, accCandidates...), abiCandidates...)
	}
	return append(append([]candidate{}, abiCandidates...), accCandidates...)
}

// abiCandidates is the self-authored fallback table: the kernel power-supply
// charge-control ABI defined in Linux 4.20 (and the charge_behaviour node added
// later), followed by a generic stop-level node.
var abiCandidates = []candidate{
	{
		label:    "charge_control threshold (kernel ABI)",
		mode:     modeThreshold,
		startPat: "*/charge_control_start_threshold",
		endPat:   "*/charge_control_end_threshold",
	},
	{
		label:      "charge_stop_level (kernel ABI)",
		mode:       modeSwitch,
		pat:        "*/charge_stop_level",
		enableVal:  "100",
		disableVal: atCapacity,
	},
	{
		label:      "charge_behaviour (kernel ABI)",
		mode:       modeSwitch,
		pat:        "*/charge_behaviour",
		enableVal:  "auto",
		disableVal: "inhibit-charge",
	},
}
