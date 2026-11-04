# The demo machine of the try environment: Alpine with sshd, which trusts Beacon's certificate
# authority (the public key of secrets/ssh-ca-key, mounted read-only at /secrets), and a small web
# server on port 8080 that stop-web breaks and start-web repairs. Agents reach it through Beacon's SSH
# channel as the account beacon, which owns the web server: no root, no password, certificates only.
FROM docker.io/library/alpine:3.22

RUN apk add --no-cache openssh-server busybox-extras
# busybox (base image) provides wget and pgrep; busybox-extras provides httpd.
# adduser -D leaves the account locked ('!' in /etc/shadow) and sshd refuses a locked account even
# with certificate auth, so the password field becomes '*': no password login, the account not locked.

COPY entrypoint.sh start-web stop-web /usr/local/bin/
COPY www/ /home/beacon/www/
RUN chmod 0755 /usr/local/bin/entrypoint.sh /usr/local/bin/start-web /usr/local/bin/stop-web \
 && adduser -D -s /bin/sh beacon \
 && sed -i 's/^beacon:!:/beacon:*:/' /etc/shadow \
 && chown -R beacon:beacon /home/beacon/www \
 && chmod 0755 /home/beacon/www

EXPOSE 22 8080
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
