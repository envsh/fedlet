#!/bin/bash
set -e
mode=$1
case "$mode" in
setup)
	ifname=$2
	pfx=$3
	/usr/sbin/sysctl -w net.inet.ip.forwarding=1
	/sbin/pfctl -d 2>/dev/null || true
	/sbin/pfctl -E -f - <<EOF
rdr pass on lo0 inet proto {udp,icmp} from any to ${pfx}0/24 -> 127.0.0.1
pass out quick on $ifname route-to (lo0 127.0.0.1) inet proto {udp,icmp} from any to ${pfx}0/24
EOF
	;;
cleanup)
	/sbin/pfctl -f /etc/pf.conf
	;;
*)
	echo "Usage: $0 setup <ifname> <vlanpfx> | cleanup"
	exit 1
	;;
esac
