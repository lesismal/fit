# Debezium's Kafka Connect with Confluent's Elasticsearch sink connector
# 14.1.2 beside its own connectors, for the mysql-es scenario: run.sh
# downloads the connector's zip, checks its sha256, and puts it here as
# es-sink.zip.
ARG DEBEZIUM_IMAGE
FROM ${DEBEZIUM_IMAGE}
COPY es-sink.zip /tmp/es-sink.zip
USER root
RUN cd /kafka/connect && unzip -q /tmp/es-sink.zip && rm /tmp/es-sink.zip
USER kafka
