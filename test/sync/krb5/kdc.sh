#!/bin/sh
# The KDC's start: the realm FIT.TEST made once, with
#
#   fituser                          password KRB5_PASSWORD, pre-authentication required
#   fitkt                            a random key, its keytab /keytabs/fitkt.keytab
#   MSSQLSvc/mssql.fit.test:1433     a random key, its keytab /keytabs/mssql.keytab
#
# every key AES (aes256-cts and aes128-cts); then krb5kdc in the foreground.
set -eu
REALM=FIT.TEST
cat >/etc/krb5.conf <<EOF
[libdefaults]
	default_realm = $REALM
[realms]
	$REALM = {
		kdc = localhost
		admin_server = localhost
	}
EOF
mkdir -p /etc/krb5kdc
cat >/etc/krb5kdc/kdc.conf <<EOF
[kdcdefaults]
	kdc_ports = 88
	kdc_tcp_ports = 88
[realms]
	$REALM = {
		supported_enctypes = aes256-cts-hmac-sha1-96:normal aes128-cts-hmac-sha1-96:normal
		master_key_type = aes256-cts-hmac-sha1-96
		max_life = 10h
	}
EOF
if [ ! -f /var/lib/krb5kdc/principal ]; then
	kdb5_util create -s -r $REALM -P fit-master-key >/dev/null
	kadmin.local -q "addprinc +requires_preauth -pw ${KRB5_PASSWORD:-Fit-krb5-pw} fituser" >/dev/null
	kadmin.local -q "addprinc -randkey fitkt" >/dev/null
	kadmin.local -q "addprinc -randkey MSSQLSvc/mssql.fit.test:1433" >/dev/null
	mkdir -p /keytabs
	kadmin.local -q "ktadd -k /keytabs/fitkt.keytab fitkt" >/dev/null
	kadmin.local -q "ktadd -k /keytabs/mssql.keytab MSSQLSvc/mssql.fit.test:1433" >/dev/null
	chmod 644 /keytabs/*
fi
echo "KDC ready"
exec krb5kdc -n
