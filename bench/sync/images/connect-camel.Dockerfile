# Debezium's Kafka Connect with Apache Camel's Kafka connectors for
# RabbitMQ (spring-rabbitmq-source) and MQTT (mqtt5-source), 4.14.5, beside
# its own connectors, for the rabbitmq-kafka and mqtt-kafka scenarios:
# run.sh downloads the connectors' packages, checks their sha256, and puts
# them here as rabbitmq.tar.gz and mqtt.tar.gz.
ARG DEBEZIUM_IMAGE
FROM ${DEBEZIUM_IMAGE}
COPY rabbitmq.tar.gz mqtt.tar.gz /tmp/
USER root
RUN cd /kafka/connect && tar xzf /tmp/rabbitmq.tar.gz && tar xzf /tmp/mqtt.tar.gz && rm -rf docs /tmp/*.tar.gz
USER kafka
