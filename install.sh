#!/bin/sh
# Installs the Edka CLI on macOS or Linux:
#
#   curl -fsSL https://edka.io/install.sh | sh
#
# The script downloads the release archive for this system, checks it against
# the SHA-256 in the release's checksums.txt, and puts `edka` in
# ~/.local/bin, where `edka upgrade` can replace it later. These variables
# change what it installs and where:
#
#   EDKA_VERSION       a release, such as v1.2.3, instead of the latest one
#   EDKA_INSTALL_DIR   the directory for edka, instead of ~/.local/bin
#   EDKA_RELEASES_URL  a mirror of https://github.com/edkadigital/cli/releases
set -eu

releases=${EDKA_RELEASES_URL:-https://github.com/edkadigital/cli/releases}
dir=${EDKA_INSTALL_DIR:-$HOME/.local/bin}

fail() {
    printf 'edka install: %s\n' "$1" >&2
    exit 1
}

case $(uname -s) in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) fail "$(uname -s) is not supported. On Windows, run install.ps1 in PowerShell." ;;
esac
case $(uname -m) in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) fail "$(uname -m) processors are not supported." ;;
esac
# A shell that Rosetta translates on Apple silicon reports x86_64.
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
    arch=arm64
fi

# A download from GitHub stays on HTTPS, redirects included.
case $releases in
    https://*) protocols='=https' ;;
    *) protocols='=http,https' ;;
esac
if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL --proto "$protocols" --proto-redir "$protocols" -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -q -O "$2" "$1"; }
else
    fail "curl or wget is needed to download the CLI."
fi
if command -v sha256sum >/dev/null 2>&1; then
    sha256() { sha256sum "$1" | cut -d ' ' -f 1; }
elif command -v shasum >/dev/null 2>&1; then
    sha256() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
else
    fail "sha256sum or shasum is needed to check the download."
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' INT TERM

# checksums.txt names each archive of the release with its version, so it
# also tells which version is the latest.
if [ -n "${EDKA_VERSION:-}" ]; then
    base=$releases/download/$EDKA_VERSION
else
    base=$releases/latest/download
fi
fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "Can't download $base/checksums.txt."
line=$(grep " edka_[^ ]*_${os}_${arch}\.tar\.gz\$" "$tmp/checksums.txt" | head -n 1 || true)
[ -n "$line" ] || fail "The release has no archive for $os/$arch."
expected=${line%% *}
archive=${line##* }
version=${archive#edka_}
version=${version%_"$os"_"$arch".tar.gz}

printf 'Downloading edka %s for %s/%s\n' "$version" "$os" "$arch"
fetch "$releases/download/$version/$archive" "$tmp/$archive" || fail "Can't download $archive."
[ "$(sha256 "$tmp/$archive")" = "$expected" ] || fail "$archive does not match its SHA-256 in checksums.txt. Nothing was installed."
tar -xzf "$tmp/$archive" -C "$tmp" edka || fail "Can't unpack $archive."

# The binary is copied next to its destination and renamed into place, so an
# edka that runs meanwhile never reads half a file.
mkdir -p "$dir" 2>/dev/null || fail "Can't create $dir. Set EDKA_INSTALL_DIR to a directory you can write to."
partial=$dir/.edka-install-$$
if ! cp "$tmp/edka" "$partial" 2>/dev/null || ! chmod 755 "$partial" || ! mv -f "$partial" "$dir/edka"; then
    rm -f "$partial"
    fail "Can't write to $dir. Set EDKA_INSTALL_DIR to a directory you can write to."
fi
printf 'Installed edka %s in %s\n' "$version" "$dir"

case :$PATH: in
    *:"$dir":*)
        found=$(command -v edka || true)
        if [ "$found" != "$dir/edka" ]; then
            printf '\n%s comes first on your PATH, so `edka` runs that one.\n' "$found"
        fi
        ;;
    *)
        printf '\n%s is not on your PATH. Add it in ~/.zshrc or ~/.bashrc:\n\n' "$dir"
        printf '  export PATH="%s:$PATH"\n' "$dir"
        found=$(command -v edka || true)
        if [ -n "$found" ]; then
            printf '\nUntil then, `edka` runs %s.\n' "$found"
        fi
        ;;
esac
printf '\nRun `edka login` to sign in.\n'
