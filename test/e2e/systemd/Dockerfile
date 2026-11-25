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
        hostname tzdata python3 python3-systemd shadow-utils findutils acl \
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

# The directory tree the files operation is listed against, shared with the
# ubuntu image. It comes from test/e2e/fixtures through the e2e build context
# docker-compose.yml names, so both images run the one script.
# The ACL it puts on one directory does not survive into the image, so
# focal-e2e-files-acl.service sets it again at boot, before sshd starts.
COPY --from=e2e fixtures/files-fixture.sh /usr/local/libexec/focal-files-fixture
COPY focal-e2e-files-acl.service /etc/systemd/system/focal-e2e-files-acl.service
RUN chmod 755 /usr/local/libexec/focal-files-fixture && /usr/local/libexec/focal-files-fixture

# A second OpenPGP public key, imported next to the one AL2023 ships. rpm keeps
# each imported key as an installed package named gpg-pubkey, so "rpm -q
# gpg-pubkey" now answers with two versions of one name: the multi-version
# output a kernel (an installonly package) gives on a real host, which this
# image has no kernel to show. The key is a throwaway public key made for this
# fixture; nothing is signed with it.
COPY focal-e2e-second-key.asc /tmp/focal-e2e-second-key.asc
RUN rpm --import /tmp/focal-e2e-second-key.asc && rm /tmp/focal-e2e-second-key.asc

COPY focal-e2e-logger /usr/local/libexec/focal-e2e-logger
COPY focal-e2e-logger.service /etc/systemd/system/focal-e2e-logger.service
RUN chmod 755 /usr/local/libexec/focal-e2e-logger \
    && systemctl enable sshd.service focal-e2e-logger.service focal-e2e-files-acl.service

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
