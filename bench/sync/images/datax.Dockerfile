# DataX 202309, its engine and its MySQL reader and writer only, from the
# release's tarball (run.sh downloads it, checks its sha256 and unpacks
# those parts into datax/), on Java 8. It runs the job mounted at
# /etc/datax/job.json as bin/datax.py would, with its default 1 GiB heap,
# without Python, then stays, so that its container can be measured to the
# end.
ARG JRE_IMAGE
FROM ${JRE_IMAGE}
COPY datax /opt/datax
CMD ["sh", "-c", "java -server -Xms1g -Xmx1g -XX:+HeapDumpOnOutOfMemoryError -Dloglevel=info -Dfile.encoding=UTF-8 -Dlogback.statusListenerClass=ch.qos.logback.core.status.NopStatusListener -Djava.security.egd=file:///dev/urandom -Ddatax.home=/opt/datax -Dlogback.configurationFile=/opt/datax/conf/logback.xml -Dlog.file.name=bench -classpath '/opt/datax/lib/*:.' com.alibaba.datax.core.Engine -mode standalone -jobid -1 -job /etc/datax/job.json; echo \"datax exited $?\"; exec sleep infinity"]
