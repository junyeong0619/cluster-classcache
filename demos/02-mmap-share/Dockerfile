FROM eclipse-temurin:22-jdk

RUN apt-get update && apt-get install -y --no-install-recommends \
        procps curl unzip \
    && rm -rf /var/lib/apt/lists/*

ARG BB_VERSION=1.14.19
RUN curl -fsSL "https://repo1.maven.org/maven2/net/bytebuddy/byte-buddy/${BB_VERSION}/byte-buddy-${BB_VERSION}.jar" \
    -o /opt/byte-buddy.jar

WORKDIR /work
COPY src/ /work/src/
COPY scripts/ /work/scripts/
RUN chmod +x /work/scripts/*.sh

# Compile + archive build (at image build time)
RUN /work/scripts/build-in-container.sh

ENV N=4
CMD ["/work/scripts/measure-in-container.sh"]
