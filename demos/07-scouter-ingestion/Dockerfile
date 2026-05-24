FROM classcache-apm-v0.1 AS base

# Add Scouter agent + conf
COPY scouter.agent.jar /opt/scouter/scouter.agent.jar
COPY scouter.conf      /opt/scouter/conf/scouter.conf

ENV SCOUTER_AGENT=/opt/scouter/scouter.agent.jar
ENV SCOUTER_CONF=/opt/scouter/conf/scouter.conf

COPY trial.sh /opt/trial.sh
RUN chmod +x /opt/trial.sh

CMD ["/opt/trial.sh"]
