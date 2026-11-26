FROM node:24.21.0-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl git openssh-server procps unzip \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /run/sshd /etc/sortie-lab \
    && useradd --create-home --shell /bin/bash --password '*' sortie

COPY sshd_config /etc/sortie-lab/sshd_config
COPY entrypoint.sh /usr/local/bin/sortie-lab-entrypoint
COPY install.sh /tmp/install.sh
RUN sh /tmp/install.sh && rm /tmp/install.sh

ENTRYPOINT ["/usr/local/bin/sortie-lab-entrypoint"]
