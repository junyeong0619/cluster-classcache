# v0.5 default: Scouter v2.21.3 as the production APM agent.
# - Build: -javaagent:scouter.agent.jar → Scouter-transformed classes baked into the archive.
# - Runtime: -Xbootclasspath/a:scouter.agent.jar → no premain, classes are just reachable.
# OTel is a separate v0.6 task (instrumentation non-determinism + isolated classloader issues).
FROM classcache-apm-v0.1 AS appbase

FROM eclipse-temurin:22-jdk

RUN apt-get update && apt-get install -y --no-install-recommends \
        procps curl python3 python3-pip python3-venv \
    && rm -rf /var/lib/apt/lists/*

RUN pip3 install --break-system-packages --no-cache-dir valkey==6.0.2

# Spring Boot extracted + fat jar (from the apm image)
COPY --from=appbase /work/app.jar       /work/app.jar
COPY --from=appbase /work/extracted     /work/extracted

# Scouter agent + conf — used both as the primer's build agent and on the workload's boot CP.
COPY scouter.agent.jar /work/agent.jar
COPY scouter.conf      /opt/scouter/conf/scouter.conf

COPY primer/primer.py /opt/primer/primer.py

ENV ARCHIVE_DIR=/var/lib/classcache
ENV APP_JAR=/work/app.jar
ENV AGENT_JAR=/work/agent.jar
ENV EXTRACT_DIR=/work/extracted
ENV VALKEY_HOST=redis
ENV VALKEY_PORT=6379
ENV PEER_PORT=8088
# Spell out Scouter's conf location (cluster-primer passes it through generically).
ENV EXTRA_JVM_OPTS=-Dscouter.config=/opt/scouter/conf/scouter.conf

CMD ["python3", "/opt/primer/primer.py"]
