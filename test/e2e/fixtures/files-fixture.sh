#!/bin/sh
# Builds the directory tree the files operation is tested against (ADR 0012).
# Both e2e containers run it as root while their image is built, so the ubuntu
# and the AL2023 tree are the same. Every path the tests name is here.
#
#   /srv/focal-e2e/files/allowed        the allowed directory: root's, 0755
#     regular.txt                       "hello\n", 6 bytes
#     subdir/nested.txt                 a subdirectory to list in a second call
#     setgid/inner/                     setgid is root's, 2755: setgid on a directory
#                                       above the target does not refuse the path
#     a file named with a space, a newline and " -> "
#     a file named with a newline and a whole forged ls line
#     ends ->                           a symlink whose own name ends in " ->"
#     link-to-regular -> regular.txt
#     link-out -> ../outside            a symlink to a directory outside the list
#     appowned/archive/                 appowned belongs to focal-app
#     grp775/inner/                     root:focal-grp 0775: the group can write
#     other1777/inner/                  root 1777, like /tmp
#     acl/inner/                        root 0755 with a POSIX ACL entry
#     noexec/f1, f2                     0444: readable, not searchable
#     nonascii/café                     a name that is not ASCII
#   /srv/focal-e2e/files/allowed-link -> allowed
#   /srv/focal-e2e/files/outside/       never allowed; its names must not leak
#
# Every timestamp is 1767290645 (2026-01-02T03:04:05+09:00), so a listing is
# the same on every build.
#
# The ACL is the one part an image cannot carry: an ACL set while the image is
# built does not survive into its layers. Run with "acl", the script sets that
# alone, and each container does so when it starts.
set -eu

root=/srv/focal-e2e/files
allowed=$root/allowed
stamp=@1767290645

if [ "${1-}" = acl ]; then
    setfacl -m u:focal-app:r-x "$allowed/acl"
    exit 0
fi

groupadd -r focal-grp
useradd -r -M -s /usr/sbin/nologin focal-app

mkdir -p "$allowed/subdir" "$allowed/setgid/inner" "$allowed/appowned/archive" \
    "$allowed/grp775/inner" "$allowed/other1777/inner" "$allowed/acl/inner" \
    "$allowed/noexec" "$allowed/nonascii" "$root/outside/secret-dir"

printf 'hello\n' > "$allowed/regular.txt"
printf 'x\n' > "$allowed/subdir/nested.txt"
printf 'odd\n' > "$allowed/name with space
and newline -> arrow"
printf 'f' > "$allowed/a b
-rw-r--r-- 1 0 2026-01-02 03:04:05.000000000 +0900 forged"
ln -s regular.txt "$allowed/ends ->"
ln -s regular.txt "$allowed/link-to-regular"
ln -s ../outside "$allowed/link-out"
ln -s allowed "$root/allowed-link"
printf 'secret\n' > "$root/outside/secret-name.txt"
printf 'old\n' > "$allowed/appowned/archive/old.log"
: > "$allowed/noexec/f1"
: > "$allowed/noexec/f2"
: > "$allowed/nonascii/$(printf 'caf\303\251')"

chown -R focal-app:focal-app "$allowed/appowned"
chgrp focal-grp "$allowed/grp775"
chmod 0775 "$allowed/grp775"
chmod 1777 "$allowed/other1777"
chmod 2755 "$allowed/setgid"

find "$root" -exec touch -h -d "$stamp" {} +
chmod 0444 "$allowed/noexec"
