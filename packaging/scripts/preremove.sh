#!/bin/sh
# On full removal (not upgrade) stop and disable the service. The argument
# convention differs between rpm ($1 == 0 on erase) and deb ($1 == "remove").
set -e
upgrade=0
case "$1" in
	0 | remove | purge) upgrade=0 ;;
	*) upgrade=1 ;;
esac
if [ "$upgrade" = "0" ] && command -v systemctl >/dev/null 2>&1; then
	systemctl disable --now ad-status-sender >/dev/null 2>&1 || true
fi
