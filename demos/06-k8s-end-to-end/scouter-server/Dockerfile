# Scouter v2.21 server is JDK 8-era code (depends on internal APIs like javax.xml.bind).
# JDK 22 throws NullPointerException (Injector.defineClass null). We run it on JDK 8.
FROM eclipse-temurin:8-jre

WORKDIR /opt/scouter-server
COPY server/ ./

CMD ["sh","-c","exec java -Xmx512m -classpath /opt/scouter-server/scouter-server-boot.jar scouter.boot.Boot /opt/scouter-server/lib"]
