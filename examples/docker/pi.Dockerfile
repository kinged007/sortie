# examples/docker/pi.Dockerfile
#
# Complete working example: Sortie + Pi CLI agent.
#
# Pi requires Node.js (>= 18), npm, and git. It authenticates through the
# provider credentials configured for the selected model. The container runs
# as a non-root user.
#
# Build:
#   docker build -f examples/docker/pi.Dockerfile -t sortie-pi .
#
# Run:
#   docker run --rm --init \
#     -e ANTHROPIC_API_KEY \
#     -v "$(pwd)/workspaces:/home/sortie/workspaces" \
#     -v "$(pwd)/WORKFLOW.md:/home/sortie/WORKFLOW.md:ro" \
#     -p 7678:7678 \
#     sortie-pi /home/sortie/WORKFLOW.md

FROM ghcr.io/sortie-ai/sortie:latest AS sortie

FROM node:24-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    git wget && \
    rm -rf /var/lib/apt/lists/*

RUN npm install -g --ignore-scripts @earendil-works/pi-coding-agent@0.85.1 && \
    npm cache clean --force

# Create a non-root user. Remove the base image's node user so the sortie
# user can own UID 1000, matching the other agent images.
RUN userdel -r node 2>/dev/null; \
    useradd --create-home --shell /bin/bash --uid 1000 sortie

COPY --from=sortie /usr/bin/sortie /usr/bin/sortie

USER sortie
WORKDIR /home/sortie

EXPOSE 7678

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO /dev/null http://localhost:7678/readyz || exit 1

ENTRYPOINT ["/usr/bin/sortie", "--host", "0.0.0.0", "--log-format", "json"]
