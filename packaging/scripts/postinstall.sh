#!/bin/sh
# Reload systemd so the shipped unit is picked up. Enabling/starting is left to
# the operator (config and credentials must be set first).
set -e
if command -v systemctl >/dev/null 2>&1; then
	systemctl daemon-reload || true
fi
