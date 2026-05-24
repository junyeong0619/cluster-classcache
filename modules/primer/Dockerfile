# v0.6 distroless primer image.
#
# Build context = repo root (build.sh handles this).
#
# Image layers:
#   1) gobuild        — go 1.22 toolchain, compiles primer statically
#   2) appbase        — classcache-apm-v0.1, source of app.jar + extracted/
#   3) distroless/jvm — final stage, JVM + primer binary + agent + profile
#
# Footprint note: the v0.6 plan targeted ~20MB. That's infeasible while the
# primer forks a JVM at runtime to bake the archive — distroless/java22 alone
# is ~80MB. The 10× reduction vs the Python image still lands; the binary
# itself is ~15MB. Splitting primer (distroless/static) from the JVM via a
# sidecar is a v0.7 option.

FROM golang:1.22 AS gobuild
WORKDIR /src
COPY modules/primer/go.mod modules/primer/go.sum* ./
RUN go mod download
COPY modules/primer/*.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -o /out/primer .

FROM classcache-apm-v0.1 AS appbase

FROM eclipse-temurin:22-jre
# distroless/java22 was retired from the registry; falling back to
# eclipse-temurin:22-jre. ~200MB instead of ~80MB. JDK 22 bytecode in the
# extracted Spring Boot jar requires a matching runtime, so we can't
# downgrade to distroless/java21 without rebuilding the app.
ARG PROFILE_PATH=modules/agent-profiles/profiles/scouter.yaml
COPY --from=gobuild /out/primer        /primer
COPY --from=appbase /work/app.jar      /work/app.jar
COPY --from=appbase /work/extracted    /work/extracted
COPY demos/04-cluster-primer/scouter.agent.jar    /work/agent.jar
COPY demos/04-cluster-primer/scouter.conf         /opt/scouter/conf/scouter.conf
COPY ${PROFILE_PATH}                              /etc/classcache/profile.yaml

ENV VALKEY_HOST=redis \
    VALKEY_PORT=6379 \
    APP_JAR=/work/app.jar \
    AGENT_JAR=/work/agent.jar \
    EXTRACT_DIR=/work/extracted \
    ARCHIVE_DIR=/var/lib/classcache \
    PEER_PORT=8088 \
    PROFILE_PATH=/etc/classcache/profile.yaml

ENTRYPOINT ["/primer"]
