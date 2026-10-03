# Installs the Edka CLI on Windows:
#
#   irm https://edka.io/install.ps1 | iex
#
# The script downloads the release archive for this system, checks it against
# the SHA-256 in the release's checksums.txt, puts edka.exe in
# %LOCALAPPDATA%\Programs\edka, and adds that directory to your PATH. These
# variables change what it installs and where:
#
#   EDKA_VERSION       a release, such as v1.2.3, instead of the latest one
#   EDKA_INSTALL_DIR   the directory for edka.exe, which the script then leaves
#                      off your PATH
#   EDKA_RELEASES_URL  a mirror of https://github.com/edkadigital/cli/releases

# `iex` runs the script in your session, so it keeps its variables and
# settings to itself, and reports a failure with throw, since exit would close
# the window.
& {
    $ErrorActionPreference = 'Stop'
    # Windows PowerShell draws a progress bar for each chunk of a download,
    # which makes it many times slower.
    $ProgressPreference = 'SilentlyContinue'
    # Windows PowerShell 5.1 offers TLS 1.2, which GitHub requires, only when
    # asked to.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $releases = if ($env:EDKA_RELEASES_URL) { $env:EDKA_RELEASES_URL } else { 'https://github.com/edkadigital/cli/releases' }
    $dir = if ($env:EDKA_INSTALL_DIR) { $env:EDKA_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\edka' }

    # 32-bit PowerShell on 64-bit Windows keeps the system's architecture in
    # PROCESSOR_ARCHITEW6432.
    $machine = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    $arch = switch ($machine) {
        'AMD64' { 'amd64' }
        'ARM64' { 'arm64' }
        default { throw "edka install: $machine processors are not supported." }
    }

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('edka-install-' + [Guid]::NewGuid())
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        # checksums.txt names each archive of the release with its version, so
        # it also tells which version is the latest.
        $base = if ($env:EDKA_VERSION) { "$releases/download/$env:EDKA_VERSION" } else { "$releases/latest/download" }
        $sums = Join-Path $tmp 'checksums.txt'
        Invoke-WebRequest -UseBasicParsing -Uri "$base/checksums.txt" -OutFile $sums
        $line = Get-Content $sums | Where-Object { $_ -match " edka_\S+_windows_$arch\.zip$" } | Select-Object -First 1
        if (-not $line) { throw "edka install: The release has no archive for windows/$arch." }
        $expected, $archive = $line -split '\s+', 2
        $version = $archive -replace '^edka_', '' -replace "_windows_$arch\.zip$", ''

        Write-Host "Downloading edka $version for windows/$arch"
        $zip = Join-Path $tmp $archive
        Invoke-WebRequest -UseBasicParsing -Uri "$releases/download/$version/$archive" -OutFile $zip
        # .NET hashes and unpacks the archive. Get-FileHash and Expand-Archive
        # live in modules that Windows PowerShell fails to load when it starts
        # from PowerShell 7, whose module path it inherits.
        $stream = [IO.File]::OpenRead($zip)
        try {
            $sha256 = [Security.Cryptography.SHA256]::Create()
            $actual = -join ($sha256.ComputeHash($stream) | ForEach-Object { $_.ToString('x2') })
        } finally {
            $stream.Dispose()
        }
        if ($actual -ne $expected) {
            throw "edka install: $archive does not match its SHA-256 in checksums.txt. Nothing was installed."
        }
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        [IO.Compression.ZipFile]::ExtractToDirectory($zip, (Join-Path $tmp 'unpacked'))

        New-Item -ItemType Directory -Force -Path $dir | Out-Null
        $target = Join-Path $dir 'edka.exe'
        # Windows can't overwrite an edka.exe that runs, but it can rename it.
        # `edka upgrade` keeps the old binary aside the same way.
        Remove-Item -Force -ErrorAction SilentlyContinue "$target.old"
        if (Test-Path $target) { Move-Item -Force $target "$target.old" }
        Copy-Item (Join-Path $tmp 'unpacked\edka.exe') $target
        Write-Host "Installed edka $version in $dir"
    } finally {
        Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $tmp
    }

    $onPath = ($env:Path -split ';') -contains $dir
    if (-not $env:EDKA_INSTALL_DIR) {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        $entries = @($userPath -split ';' | Where-Object { $_ })
        if ($entries -notcontains $dir) {
            [Environment]::SetEnvironmentVariable('Path', (($entries + $dir) -join ';'), 'User')
            Write-Host "Added $dir to your PATH. Terminals that were open before need a restart to find edka."
        }
        if (-not $onPath) { $env:Path = "$env:Path;$dir" }
    } elseif (-not $onPath) {
        Write-Host "$dir is not on your PATH."
    }
    Write-Host ''
    Write-Host 'Run `edka login` to sign in.'
}
