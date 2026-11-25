# The sshd container the E2E suite inspects. It is an ordinary Ubuntu host with
# an OpenSSH server and the small set of tools focal's operations shell out to
# (procps, iproute2, util-linux). This ubuntu container deliberately has no
# systemd, so here the service and logs operations report a failed inspection
# rather than data — the suite asserts exactly that. Checks that need systemd
# run against the separate AL2023 container in test/e2e/systemd instead.
FROM ubuntu:24.04

# The e2e user's authorized key. It is passed in rather than baked as a file so
# the build context stays free of a generated secret: the harness makes a fresh
# ed25519 pair per run and hands the public half here.
ARG PUBKEY

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        openssh-server procps iproute2 util-linux ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir /run/sshd \
    && useradd -m -s /bin/bash e2e \
    && mkdir -p /home/e2e/.ssh \
    && printf '%s\n' "$PUBKEY" > /home/e2e/.ssh/authorized_keys \
    && chown -R e2e:e2e /home/e2e/.ssh \
    && chmod 700 /home/e2e/.ssh \
    && chmod 600 /home/e2e/.ssh/authorized_keys

# nano is installed and then removed without --purge, which leaves it in dpkg's
# database in the "rc" state: not installed, but its configuration is still
# recorded. The package operation has to report that as not installed.
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nano \
    && DEBIAN_FRONTEND=noninteractive apt-get remove -y nano \
    && rm -rf /var/lib/apt/lists/*

EXPOSE 22

# `docker compose up --wait` blocks until this reports healthy, so the harness
# never races the SSH server's startup. ss ships with iproute2, already
# installed above.
HEALTHCHECK --interval=1s --timeout=3s --retries=60 --start-period=1s \
    CMD ss -H -ltn 'sport = :22' | grep -q LISTEN || exit 1

CMD ["/usr/sbin/sshd", "-D", "-e"]
