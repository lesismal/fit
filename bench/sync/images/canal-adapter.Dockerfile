# Canal's client adapter 1.1.8, from its release's tarball (run.sh downloads
# it, checks its sha256 and unpacks it into canal-adapter/), on Java 8. It
# runs in the foreground with the heap its bin/startup.sh gives it on a
# 64-bit Java 8; its config is mounted at conf/application.yml and
# conf/es7/items.yml. The release's example mappings are removed, since the
# adapter loads every file of its mapping folders (and refuses the whole
# adapter for one it cannot read, as macOS tar's ._ files).
ARG JRE_IMAGE
FROM ${JRE_IMAGE}
COPY canal-adapter /opt/canal-adapter
RUN cd /opt/canal-adapter/conf && rm -rf es6/* es7/* es8/* rdb/* hbase/* kudu/* tablestore/*
WORKDIR /opt/canal-adapter/bin
CMD ["sh", "-c", "exec java -server -Xms2g -Xmx3g -Xmn1g -XX:SurvivorRatio=2 -XX:MaxTenuringThreshold=15 -XX:+DisableExplicitGC -Xss1m -XX:-OmitStackTraceInFastThrow -Djava.awt.headless=true -Djava.net.preferIPv4Stack=true -Dfile.encoding=UTF-8 -DappName=canal-adapter -classpath '.:/opt/canal-adapter/conf:/opt/canal-adapter/lib/*' com.alibaba.otter.canal.adapter.launcher.CanalAdapterApplication"]
