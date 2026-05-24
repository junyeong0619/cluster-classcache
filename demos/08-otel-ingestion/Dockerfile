FROM classcache-apm-v0.1 AS base
COPY opentelemetry-javaagent.jar /opt/otel/opentelemetry-javaagent.jar
COPY trial.sh /opt/trial.sh
RUN chmod +x /opt/trial.sh
CMD ["/opt/trial.sh"]
