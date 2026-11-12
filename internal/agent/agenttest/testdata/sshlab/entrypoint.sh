#!/bin/sh
set -eu

ssh-keygen -A >/dev/null
printf '%s\n' "$SORTIE_LAB_AUTHORIZED_KEY" >/etc/sortie-lab/authorized_keys

printf '%s\n' "$SORTIE_LAB_DIRS" | while IFS= read -r dir; do
	[ -n "$dir" ] || continue
	top=$dir
	while [ ! -e "$(dirname -- "$top")" ]; do
		top=$(dirname -- "$top")
	done
	mkdir -p -- "$dir"
	chown -R sortie:sortie -- "$top"
done

/usr/sbin/sshd -D -e -f /etc/sortie-lab/sshd_config -p "$SORTIE_LAB_REFUSED_PORT" -o AllowTcpForwarding=no &
exec /usr/sbin/sshd -D -e -f /etc/sortie-lab/sshd_config -p "$SORTIE_LAB_PORT" -o AllowTcpForwarding=yes
