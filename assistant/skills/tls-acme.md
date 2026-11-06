---
name: tls-acme
title: TLS and ACME
title_zh: TLS 与 ACME
description: Certificates by ACME or from files, HTTPS by default, redirects to HTTPS and client certificates.
description_zh: 通过 ACME 或文件配置证书、默认 HTTPS、跳转到 HTTPS 以及客户端证书。
tools: [config_get, schema_describe, config_validate, certs_list, status_get, config_propose]
inputs:
  - name: hosts
    description: The host names to serve over HTTPS, comma separated.
  - name: email
    description: The ACME account's contact email.
examples:
  - Serve example.com and www.example.com over HTTPS with Let's Encrypt.
  - Use the certificate in /etc/ssl/site.pem for the web listener.
  - Require client certificates on /admin/*.
---
You set up TLS for this fit: certificates, HTTPS listeners and redirects.

1. Read: `config_get` with `members: ["listeners", "routes", "acme", "auto_https"]`, and `certs_list`
   for what is served now (names, issuer, days left, ACME state).
2. Decide with the person, by `ask_user` when it is not said: certificates from ACME (the host names
   must resolve to this machine and port 80 or 443 reach it) or from files (the paths).
3. Work it out with `schema_describe` on `acme`, `auto_https` and `listeners.tls`:
   - ACME: `acme.email`, the challenge that fits, and the listener's `tls.acme` names;
     `auto_https` to have fit add the redirects and certificates by itself;
   - files: the listener's `tls.certificates` with `cert_file` and `key_file` (the paths, never the
     key's content);
   - a redirect from http to https for the same hosts, unless `auto_https` adds it;
   - client certificates: the listener's `tls.client_auth` with the CA files, and the
     `client_cert` middleware on the routes that require one.
4. Never put a private key into the config, and never write a `[redacted]` value anywhere new.
5. Check with `config_validate`, then `config_propose`; say what a browser will see once it is
   applied, and how long ACME takes for a first certificate.

Without the propose tools, show the config text instead.
