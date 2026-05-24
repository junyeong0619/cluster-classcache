# --- Build stage ----------------------------------------------------
FROM maven:3.9-eclipse-temurin-22 AS build

ARG BB_VERSION=1.14.19
RUN curl -fsSL "https://repo1.maven.org/maven2/net/bytebuddy/byte-buddy/${BB_VERSION}/byte-buddy-${BB_VERSION}.jar" \
    -o /opt/byte-buddy.jar

WORKDIR /build

# Spring Boot app
COPY app/pom.xml /build/app/pom.xml
RUN cd /build/app && mvn -B -q dependency:go-offline
COPY app/src /build/app/src
RUN cd /build/app && mvn -B -q package -DskipTests

# Agent (built directly with javac)
COPY agent/src /build/agent/src
RUN mkdir -p /build/agent/classes /build/agent/shade /build/agent/shade/META-INF \
 && javac -cp /opt/byte-buddy.jar -d /build/agent/classes \
        /build/agent/src/main/java/com/example/agent/TraceAdvice.java \
        /build/agent/src/main/java/com/example/agent/TraceAgent.java \
 && cd /build/agent/shade && jar -xf /opt/byte-buddy.jar \
 && cp -r /build/agent/classes/* . \
 && printf "Manifest-Version: 1.0\nPremain-Class: com.example.agent.TraceAgent\nCan-Retransform-Classes: false\nCan-Redefine-Classes: false\n" > META-INF/MANIFEST.MF \
 && jar cfm /build/agent/agent.jar META-INF/MANIFEST.MF .

# --- Runtime stage --------------------------------------------------
FROM eclipse-temurin:22-jdk

RUN apt-get update && apt-get install -y --no-install-recommends \
        procps curl coreutils \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /work
COPY --from=build /build/app/target/app.jar  /work/app.jar
COPY --from=build /build/agent/agent.jar     /work/agent.jar
COPY scripts/ /work/scripts/
RUN chmod +x /work/scripts/*.sh

# Build the archive (baked into the image)
RUN /work/scripts/build-archive-in-container.sh

ENV N=4
CMD ["/work/scripts/measure-in-container.sh"]
