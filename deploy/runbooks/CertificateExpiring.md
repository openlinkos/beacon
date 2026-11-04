# CertificateExpiring: a certificate is about to expire

## What it means

The earliest-expiring certificate any probe location sees for the service (`probe_ssl_earliest_cert_expiry`) runs out within 14 days (warning) or 7 days (critical). Automatic renewal has not happened: Let's Encrypt renews 30 days ahead, so 14 days left means it has failed more than once.

## What to check

1. Whatever issues the certificate: Caddy's or Traefik's log, `certbot certificates`, cert-manager's `Certificate` and `Order` objects, the CDN's certificate page.
2. Common causes: the ACME challenge blocked (port 80 closed, a DNS record moved, a CAA record), an account rate limit, a certificate bought by hand that nobody renewed.
3. Which certificate the probes actually see: `openssl s_client -connect <host>:443 -servername <host> </dev/null 2>/dev/null | openssl x509 -noout -dates`. A CDN in front may serve its own.

## How to fix

Renew it (or let the issuer retry once the cause is gone), then make sure the renewed certificate is what is served: a reload of the proxy, a restarted pod.

## How to verify

The next probe round sees the new expiry; the alert resolves after `resolve_timeout`.

To silence: the card's buttons; `silence_alert` over MCP; or `beacon silence create -matchers 'alertname=CertificateExpiring,service=<service>' -duration 24h -comment '…'`.
