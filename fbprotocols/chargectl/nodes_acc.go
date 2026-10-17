//go:build linux || android

package chargectl

// accCandidates is a re-typed subset of the switch table from the ACC project
// (Advanced Charging Controller), install/ctrl-files.sh:
//
//	https://github.com/VR-25/acc
//
// ACC is GPL-3.0; only hardware node names and their allow/stop values are
// reproduced here. Entries that merely tune current/voltage/temperature, that
// enable fast charge, or that are marked experimental/deprecated upstream are
// left out — this backend only ever pauses and resumes charging. Relative
// patterns are rooted at power_supply; patterns beginning with / are absolute.
// The first value is written to allow charging, the second to stop it.
var accCandidates = []candidate{
	// Generic kernel switch nodes, matched on any supply directory.
	{label: "*/*charging_enable*", mode: modeSwitch, pat: "*/*charging_enable*", enableVal: "1", disableVal: "0"},
	{label: "*/enable_charg*", mode: modeSwitch, pat: "*/enable_charg*", enableVal: "1", disableVal: "0"},
	{label: "*/charge_enabled", mode: modeSwitch, pat: "*/charge_enabled", enableVal: "1", disableVal: "0"},
	{label: "*/charge_disable", mode: modeSwitch, pat: "*/charge_disable", enableVal: "0", disableVal: "1"},
	{label: "*/disable_charg*", mode: modeSwitch, pat: "*/*disable_charg*", enableVal: "0", disableVal: "1"},
	{label: "*/charger_control", mode: modeSwitch, pat: "*/charger_control", enableVal: "1", disableVal: "0"},
	{label: "*/charging_state", mode: modeSwitch, pat: "*/charging_state", enableVal: "enabled", disableVal: "disabled"},
	{label: "*/input_suspend", mode: modeSwitch, pat: "*/input_suspend", enableVal: "0", disableVal: "1"},

	// Nodes under the supply named "battery" (typical Android layout).
	{label: "battery/charging_enabled", mode: modeSwitch, pat: "battery/charging_enabled", enableVal: "1", disableVal: "0"},
	{label: "battery/ChargerEnable", mode: modeSwitch, pat: "battery/ChargerEnable", enableVal: "1", disableVal: "0"},
	{label: "battery/op_disable_charge", mode: modeSwitch, pat: "battery/op_disable_charge", enableVal: "0", disableVal: "1"},
	{label: "battery/batt_slate_mode", mode: modeSwitch, pat: "battery/batt_slate_mode", enableVal: "0", disableVal: "1"},
	{label: "battery/battery_input_suspend", mode: modeSwitch, pat: "battery/battery_input_suspend", enableVal: "0", disableVal: "1"},
	// Samsung store_mode freezes the level where it was triggered (recovers only
	// after the cable is pulled for a few seconds); ACC disables it by default.
	// Keep it behind batt_slate_mode.
	{label: "battery/store_mode", mode: modeSwitch, pat: "battery/store_mode", enableVal: "0", disableVal: "1"},
	{label: "battery/restricted_charging", mode: modeSwitch, pat: "battery/restricted_charging", enableVal: "0", disableVal: "1"},
	{label: "battery/siop_level", mode: modeSwitch, pat: "battery/siop_level", enableVal: "100", disableVal: "0"},
	{label: "battery/device/*stop_charging", mode: modeSwitch, pat: "battery/device/*stop_charging", enableVal: "0", disableVal: "1"},
	{label: "battery/device/Charging_Enable", mode: modeSwitch, pat: "battery/device/Charging_Enable", enableVal: "1", disableVal: "0"},
	{label: "battery_ext/smart_charging_interruption", mode: modeSwitch, pat: "battery_ext/smart_charging_interruption", enableVal: "0", disableVal: "1"},

	// Vendor trees, addressed by absolute path.
	{label: "/sys/class/hw_power/charger/charge_data/enable_charger", mode: modeSwitch, pat: "/sys/class/hw_power/charger/charge_data/enable_charger", enableVal: "1", disableVal: "0"},
	{label: "/sys/class/qcom-battery/input_suspend", mode: modeSwitch, pat: "/sys/class/qcom-battery/input_suspend", enableVal: "0", disableVal: "1"},
	{label: "/sys/class/qcom-battery/charging_enabled", mode: modeSwitch, pat: "/sys/class/qcom-battery/charging_enabled", enableVal: "1", disableVal: "0"},
	{label: "/sys/devices/platform/huawei_charger/enable_charger", mode: modeSwitch, pat: "/sys/devices/platform/huawei_charger/enable_charger", enableVal: "1", disableVal: "0"},
	{label: "/sys/devices/platform/lge-unified-nodes/charging_enable", mode: modeSwitch, pat: "/sys/devices/platform/lge-unified-nodes/charging_enable", enableVal: "1", disableVal: "0"},
	{label: "/sys/devices/platform/lge-unified-nodes/charging_completed", mode: modeSwitch, pat: "/sys/devices/platform/lge-unified-nodes/charging_completed", enableVal: "0", disableVal: "1"},
	{label: "/sys/devices/platform/mt-battery/disable_charger", mode: modeSwitch, pat: "/sys/devices/platform/mt-battery/disable_charger", enableVal: "0", disableVal: "1"},
	{label: "/sys/devices/platform/battery/ChargerEnable", mode: modeSwitch, pat: "/sys/devices/platform/battery/ChargerEnable", enableVal: "1", disableVal: "0"},
	{label: "/sys/devices/platform/soc/soc:google,charger/charge_disable", mode: modeSwitch, pat: "/sys/devices/platform/soc/soc:google,charger/charge_disable", enableVal: "0", disableVal: "1"},
	{label: "/sys/devices/platform/soc/soc:oplus,chg_intf/oplus_chg/battery/*charging_enable", mode: modeSwitch, pat: "/sys/devices/platform/soc/soc:oplus,chg_intf/oplus_chg/battery/*charging_enable", enableVal: "1", disableVal: "0"},
	{label: "/sys/devices/platform/soc/soc:qcom,pmic_glink/.../force_charger_suspend", mode: modeSwitch, pat: "/sys/devices/platform/soc/soc:qcom,pmic_glink/soc:qcom,pmic_glink:qcom,battery_charger/force_charger_suspend", enableVal: "0", disableVal: "1"},
	{label: "/sys/devices/soc/soc:lge,*/lge_power/lge_*/charging_enabled", mode: modeSwitch, pat: "/sys/devices/soc/soc:lge,*/lge_power/lge_*/charging_enabled", enableVal: "1", disableVal: "0"},
	{label: "/sys/devices/virtual/oplus_chg/battery/*charging_enable", mode: modeSwitch, pat: "/sys/devices/virtual/oplus_chg/battery/*charging_enable", enableVal: "1", disableVal: "0"},
	{label: "/sys/kernel/debug/google_charger/chg_suspend", mode: modeSwitch, pat: "/sys/kernel/debug/google_charger/chg_suspend", enableVal: "0", disableVal: "1"},
	{label: "/sys/kernel/debug/google_charger/input_suspend", mode: modeSwitch, pat: "/sys/kernel/debug/google_charger/input_suspend", enableVal: "0", disableVal: "1"},
	{label: "/sys/module/pm*_charger/parameters/disabled", mode: modeSwitch, pat: "/sys/module/pm*_charger/parameters/disabled", enableVal: "0", disableVal: "1"},

	// Stop-level nodes: an end-only limit, same semantics as
	// charge_control_end_threshold. Writing 75 makes the pack charge to 75% and
	// stop, then recharge whenever it drops below, so no resume write is needed.
	{label: "/sys/devices/platform/google,charger/charge_stop_level", mode: modeLevel, endPat: "/sys/devices/platform/google,charger/charge_stop_level"},
	{label: "/sys/module/lge_battery/parameters/charge_stop_level", mode: modeLevel, endPat: "/sys/module/lge_battery/parameters/charge_stop_level"},
}
