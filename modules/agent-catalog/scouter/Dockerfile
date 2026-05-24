# Catalog image for the Scouter v2.21.3 APM agent.
#
# Purpose: ship the agent jar + conf at well-known paths so the operator's
# cc-extract-agent initContainer can `cp /agent.jar /opt/agent/agent.jar`
# and `cp /agent.conf /opt/agent/agent.conf` without any per-app build.
#
# The image needs a shell (for the cp command) and nothing else. Alpine
# keeps the footprint tiny (~15MB total including the jar).
FROM alpine:3.20
COPY scouter.agent.jar /agent.jar
COPY scouter.conf      /agent.conf
