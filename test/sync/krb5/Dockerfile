# An MIT Kerberos KDC for the SQL Server client's Kerberos interop
# (test/sync/mssql.sh): the realm FIT.TEST, AES keys only, its keytabs
# written to /keytabs (see kdc.sh).
FROM debian:bookworm-slim
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
	krb5-kdc krb5-admin-server krb5-user && rm -rf /var/lib/apt/lists/*
COPY kdc.sh /kdc.sh
EXPOSE 88
ENTRYPOINT ["/bin/sh", "/kdc.sh"]
