#!/bin/bash
# fedpubhttp - Publish messages via HTTP POST to a P2P node
# Usage:
#   pubrest.sh -msg "hello world"
#   pubrest.sh -channel news -file /path/to/data.bin
#   pubrest.sh -feduri http://127.0.0.1:4004 -channel reddit -msg "hi"

feduri=http://127.0.0.1:4004
channel=reddit
msg=
file=

show_help() {
  cat <<EOF
Usage: $(basename "$0") [options]

  -feduri URL     P2P node address (default: http://127.0.0.1:4004)
  -channel NAME   Topic name (default: reddit)
  -msg TEXT       Message content (mutually exclusive with -file)
  -file PATH      Path to data file (max 5MB, mutually exclusive with -msg)
  -h              Show this help
EOF
}

for arg in "$@"; do
  if [ "$arg" = "-h" ]; then
    show_help
    exit 0
  fi
done

if [ $# -eq 0 ]; then
  show_help
  exit 0
fi

while [ $# -gt 0 ]; do
  case "$1" in
    -feduri) feduri="$2"; shift 2 ;;
    -channel) channel="$2"; shift 2 ;;
    -msg) msg="$2"; shift 2 ;;
    -file) file="$2"; shift 2 ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
done

if [ -n "$msg" ] && [ -n "$file" ]; then
  echo "error: -msg and -file are mutually exclusive" >&2
  exit 1
fi

if [ -z "$msg" ] && [ -z "$file" ]; then
  echo "error: must provide either -msg or -file" >&2
  exit 1
fi

if [ -n "$file" ]; then
  if [ ! -f "$file" ]; then
    echo "error: file not found: $file" >&2
    exit 1
  fi
  size=$(stat -c%s "$file" 2>/dev/null || stat -f%z "$file" 2>/dev/null)
  if [ "$size" -gt 5242880 ]; then
    echo "error: file exceeds 5MB limit" >&2
    exit 1
  fi
fi

url="${feduri%/}/p2pin/send?topic=$channel"

if [ -n "$msg" ]; then
  curl -sS -X POST -H "Content-Type: application/octet-stream" \
    --data "$msg" "$url"
  exit $?
else
  curl -sS -X POST -H "Content-Type: application/octet-stream" \
    --data-binary "@$file" "$url"
  exit $?
fi
