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
        openssh-server procps iproute2 util-linux ca-certificates acl \
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

# The directory tree the files operation is listed against, shared with the
# AL2023 image (fixtures/files-fixture.sh). acl, installed above, is what lets
# it put a POSIX ACL on one directory, which happens when the container starts
# (CMD below): an ACL set during the build does not survive into the image. A
# failure there leaves sshd to start anyway, so only the ACL test fails, the way
# a failed unit leaves the AL2023 container degraded rather than down.
COPY fixtures/files-fixture.sh /usr/local/libexec/focal-files-fixture
RUN chmod 755 /usr/local/libexec/focal-files-fixture && /usr/local/libexec/focal-files-fixture

# Two more logins for the same key, whose sessions sshd gives an environment
# that would change ls's output: BLOCK_SIZE and LS_BLOCK_SIZE set to human,
# which would print sizes as 4.0K, and a fixed locale — C for e2e-env, C.UTF-8
# for e2e-utf8. In C, ls -b escapes a non-ASCII name; in C.UTF-8 it prints it
# as it is. The files tests list the same directories as each user. The Match
# blocks go last in sshd_config, where they cannot swallow a setting after
# them, and SetEnv overrides whatever the client sends.
RUN for u in e2e-env e2e-utf8; do \
        useradd -m -s /bin/bash "$u" \
        && mkdir -p "/home/$u/.ssh" \
        && printf '%s\n' "$PUBKEY" > "/home/$u/.ssh/authorized_keys" \
        && chown -R "$u:$u" "/home/$u/.ssh" \
        && chmod 700 "/home/$u/.ssh" \
        && chmod 600 "/home/$u/.ssh/authorized_keys" || exit 1; \
    done \
    && printf '\nMatch User e2e-env\n    SetEnv BLOCK_SIZE=human LS_BLOCK_SIZE=human LC_ALL=C\n' >> /etc/ssh/sshd_config \
    && printf 'Match User e2e-utf8\n    SetEnv BLOCK_SIZE=human LS_BLOCK_SIZE=human LC_ALL=C.UTF-8\n' >> /etc/ssh/sshd_config

EXPOSE 22

# `docker compose up --wait` blocks until this reports healthy, so the harness
# never races the SSH server's startup. ss ships with iproute2, already
# installed above.
HEALTHCHECK --interval=1s --timeout=3s --retries=60 --start-period=1s \
    CMD ss -H -ltn 'sport = :22' | grep -q LISTEN || exit 1

CMD ["sh", "-c", "/usr/local/libexec/focal-files-fixture acl; exec /usr/sbin/sshd -D -e"]
