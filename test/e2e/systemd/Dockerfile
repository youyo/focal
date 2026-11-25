# The second sshd container the E2E suite inspects: Amazon Linux 2023 with
# systemd running as PID 1, so the service and logs operations meet a real
# systemd and journald, and AL2023's own userland (rpm, its coreutils, its
# journalctl) answers the inspections. The ubuntu container next door has no
# systemd; anything that needs one is tested here instead.
#
# It runs without --privileged and adds no capabilities. systemd needs to write
# its own cgroup subtree, which docker-compose.yml grants with a private cgroup
# namespace and the writable-cgroups security option; /run, /run/lock and /tmp
# are tmpfs mounts there as well.
#
# The base is pinned by the digest of its multi-arch index so a rebuilt AL2023
# image cannot change what the suite runs against.
FROM amazonlinux:2023@sha256:edd3297df7d444ab9a7896b763d5305d4bab57d2fb3943e831c1713b97d32d9d

# The container image ships coreutils-single; an EC2 AL2023 host has the full
# coreutils package, so swap it in (--allowerasing) to read the same ls and
# realpath an operator's host runs. The container image also lacks hostname,
# which an EC2 AL2023 host has and the system operation runs. python3-systemd is
# only for the logger fixture below.
RUN dnf install -y --allowerasing \
        systemd openssh-server procps-ng iproute util-linux coreutils rpm \
        hostname tzdata python3 python3-systemd shadow-utils findutils \
    && dnf clean all \
    && rm -rf /var/cache/dnf

# A host clock that is not UTC, so a timestamp whose zone is lost or assumed
# shows up as a wrong offset rather than passing by coincidence. /etc/localtime
# is what both PID 1 and every SSH session read; a TZ variable on the container
# would reach only PID 1.
RUN ln -sf ../usr/share/zoneinfo/Asia/Tokyo /etc/localtime

# A directory of known entries for listing tools: a regular file, a
# subdirectory, a symlink, and a file whose name holds a space, a newline and
# " -> ". Every timestamp is a fixed epoch so a listing is the same on every
# build.
RUN python3 - <<'EOF'
import os
root = "/srv/focal-e2e/ls-fixture"
os.makedirs(root + "/subdir")
with open(root + "/regular.txt", "w") as f:
    f.write("hello\n")
with open(root + "/name with space\nand newline -> arrow", "w") as f:
    f.write("odd\n")
os.symlink("regular.txt", root + "/link-to-regular")
stamp = 1767290645  # 2026-01-02T03:04:05+09:00
for name in os.listdir(root):
    path = os.path.join(root, name)
    os.utime(path, (stamp, stamp), follow_symlinks=False)
os.utime(root, (stamp, stamp))
EOF

COPY focal-e2e-logger /usr/local/libexec/focal-e2e-logger
COPY focal-e2e-logger.service /etc/systemd/system/focal-e2e-logger.service
RUN chmod 755 /usr/local/libexec/focal-e2e-logger \
    && systemctl enable sshd.service focal-e2e-logger.service

# The e2e user's authorized key, passed in the same way as for the ubuntu
# container: the harness makes a fresh ed25519 pair per run. It is declared
# here, after every other step, because each RUN after an ARG sees it in its
# environment: declared earlier, a new key every run would rebuild the dnf
# layer every run as well.
ARG PUBKEY

# The e2e user is in systemd-journal, the group that may read the system
# journal. Without it journalctl answers an unprivileged user with no entries
# and exit 0, which is exactly the empty-but-ok result the logs test refuses.
RUN useradd -m -s /bin/bash -G systemd-journal e2e \
    && mkdir -p /home/e2e/.ssh \
    && printf '%s\n' "$PUBKEY" > /home/e2e/.ssh/authorized_keys \
    && chown -R e2e:e2e /home/e2e/.ssh \
    && chmod 700 /home/e2e/.ssh \
    && chmod 600 /home/e2e/.ssh/authorized_keys

EXPOSE 22

# `docker compose up --wait` blocks until this reports healthy. Healthy means
# systemd finished booting (running, or degraded when some unrelated unit could
# not start in a container) and sshd is listening. focal-e2e-logger is ordered
# before multi-user.target, so by then its journal entries exist too.
# is-system-running exits non-zero for degraded, so the state it prints is
# compared instead of its exit status. A boot that never finishes turns the
# container unhealthy after about two minutes (12 probes of up to 10s), which
# ends `up --wait` with an error instead of leaving it waiting.
HEALTHCHECK --interval=1s --timeout=10s --retries=12 --start-period=1s \
    CMD state=$(systemctl is-system-running --wait); \
        { [ "$state" = running ] || [ "$state" = degraded ]; } \
        && ss -H -ltn 'sport = :22' | grep -q LISTEN || exit 1

# systemd shuts down cleanly on SIGRTMIN+3, not the default SIGTERM.
STOPSIGNAL SIGRTMIN+3

CMD ["/usr/sbin/init"]
