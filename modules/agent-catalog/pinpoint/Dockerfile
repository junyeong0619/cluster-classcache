# Catalog image for the Pinpoint Java agent.
#
# Pinpoint is multi-file (bootstrap jar + lib/ + plugin/ + boot/ + conf),
# so this image is structured around a single well-known directory at /agent.
# setup.sh downloads the upstream tarball, extracts it, and lays it out as:
#
#   /agent.jar       → symlink to pinpoint-bootstrap-<VER>.jar (well-known path)
#   /agent.conf      → symlink to profiles/release/pinpoint-root.config
#   /agent/          → full pinpoint-agent-<VER>/ tree
#
# The operator's cc-extract-agent initContainer copies the whole /agent
# directory into /cc-staging/agent, so plugin/* and lib/* land alongside
# the bootstrap jar at runtime.
FROM alpine:3.20
COPY agent/ /agent/
RUN ln -s /agent/pinpoint-bootstrap.jar /agent.jar \
 && ln -s /agent/profiles/release/pinpoint-root.config /agent.conf
