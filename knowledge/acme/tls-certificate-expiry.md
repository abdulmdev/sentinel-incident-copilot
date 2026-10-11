# TLS certificate expiry

Owner: security-engineering.

## Symptoms
Clients report `certificate has expired` or handshake failures; errors begin at an exact time across all regions.

## Mitigation
Certificates are issued by cert-manager. Force renewal with `cmctl renew <certificate>` and confirm the new expiry with `openssl s_client -connect host:443`.
