#!/bin/bash
mode=$1
case "$mode" in
setup)
	ifname=$2
	pfx=$3
	verify_ip=$4

	# 1. sysctl
	if ! /usr/sbin/sysctl -w net.inet.ip.forwarding=1 >/dev/null 2>&1; then
		echo "sysctl: failed to enable ip forwarding"
		exit 1
	fi

	# 2. route add
	if ! /sbin/route -q -n add -net ${pfx}0/24 -interface ${ifname} 2>&1; then
		echo "route: add -net ${pfx}0/24 via ${ifname} failed"
		exit 2
	fi

	# 3. verify
	rget=$(/sbin/route -n get ${verify_ip} 2>&1) || {
		echo "route: get ${verify_ip} failed"
		echo "$rget"
		exit 3
	}
	if ! echo "$rget" | grep -qE "(interface|gateway): ${ifname}"; then
		echo "route: verify FAIL ${verify_ip} — expected ${ifname}"
		echo "$rget"
		exit 4
	fi

	echo "route: verify OK ${verify_ip} via ${ifname}"
	;;
cleanup)
	/sbin/route -q -n delete -net ${pfx}0/24 2>/dev/null || true
	;;
*)
	echo "Usage: $0 setup <ifname> <vlanpfx> <verify_ip> | cleanup"
	exit 1
	;;
esac
