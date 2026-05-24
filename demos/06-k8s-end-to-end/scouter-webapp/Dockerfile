# Scouter webapp is also JDK 8-era code. Same JAXB internal API problems on JDK 22.
FROM eclipse-temurin:8-jre

WORKDIR /opt/scouter-webapp
COPY webapp/ ./

EXPOSE 6188

CMD ["sh","-c","exec java -cp /opt/scouter-webapp/scouter.webapp.jar:/opt/scouter-webapp/lib/* scouterx.webapp.main.WebAppMain"]
