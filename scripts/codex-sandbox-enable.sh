#!/bin/sh
# Let the Codex bubblewrap sandbox create user namespaces on an Ubuntu runner.
#
# Ubuntu 24.04 restricts unprivileged user namespaces through AppArmor. The
# workspaceWrite sandbox then cannot bring up loopback, Codex classifies every
# shell command as a sandbox denial and reports no item for it, so the
# orchestrator never sees that tool's result.
#
# Usage: codex-sandbox-enable.sh (needs sudo; a sysctl the kernel lacks is skipped)

set -eu

if [ -f /proc/sys/kernel/unprivileged_userns_clone ]; then
	sudo sysctl -w kernel.unprivileged_userns_clone=1
fi
if [ -f /proc/sys/kernel/apparmor_restrict_unprivileged_userns ]; then
	sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
fi
