# Install the SePay CLI on Windows from GitHub Releases.
#
#   irm https://get.sepay.vn/sepay-cli-install.ps1 | iex
#   $env:SEPAY_VERSION = '0.2.0'; irm https://get.sepay.vn/sepay-cli-install.ps1 | iex
#
# Options (environment variables; `iex` takes no arguments):
#   SEPAY_VERSION            version to install, e.g. 0.2.0 (default: the latest release)
#   SEPAY_INSTALL_DIR        directory for sepay.exe (default: %LOCALAPPDATA%\Programs\sepay)
#   SEPAY_NO_MODIFY_PATH=1   leave the user PATH alone
#   SEPAY_REQUIRE_SIGNATURE=1  refuse to install without cosign
#
# The archive must match its SHA-256 in checksums.txt. When cosign is on PATH, checksums.txt
# must also carry the signature of this repository's release workflow. Sends no telemetry.
# Runs on Windows PowerShell 5.1 and PowerShell 7. Keep this file ASCII: 5.1 reads a file
# without a BOM in the ANSI code page.
#
# Everything runs inside Install-Sepay, called at the end, so a download cut short runs nothing.
# Errors throw instead of calling exit: under `iex`, exit would close the user's window.

function Install-Sepay {
    $ErrorActionPreference = 'Stop'
    # Windows PowerShell 5.1 redraws a progress bar per chunk, which makes downloads crawl.
    $ProgressPreference = 'SilentlyContinue'
    $repo = 'sepayvn/sepay-cli'

    function Write-Step([string]$Message) {
        [Console]::Error.WriteLine("sepay-install: $Message")
    }

    # The version lands in a URL and on the terminal, so only release-shaped strings pass.
    # \z, not $: .NET's $ also matches before a trailing newline.
    function Test-Version([string]$Value) {
        return $Value -cmatch '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?\z'
    }

    # Follows redirects by hand so a redirect to plain http is refused, which neither
    # Invoke-WebRequest nor WebClient does on Windows PowerShell 5.1.
    function Save-Url([System.Net.Http.HttpClient]$Client, [string]$Url, [string]$Path) {
        $uri = [Uri]$Url
        for ($hop = 0; $hop -le 10; $hop++) {
            if ($uri.Scheme -ne 'https') {
                throw "refusing to download over $($uri.Scheme): $uri"
            }
            $resp = $Client.GetAsync($uri, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
            try {
                $code = [int]$resp.StatusCode
                if ($code -ge 300 -and $code -lt 400 -and $null -ne $resp.Headers.Location) {
                    $uri = New-Object Uri($uri, $resp.Headers.Location)
                    continue
                }
                if (-not $resp.IsSuccessStatusCode) {
                    throw "$Url answered $code"
                }
                $body = $resp.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
                $file = [IO.File]::Create($Path)
                try { $body.CopyTo($file) } finally { $file.Dispose(); $body.Dispose() }
                return
            } finally {
                $resp.Dispose()
            }
        }
        throw "too many redirects for $Url"
    }

    # Writes HKCU\Environment directly to keep the value's REG_EXPAND_SZ kind:
    # [Environment]::SetEnvironmentVariable would store %VARS% already expanded.
    function Add-UserPath([string]$Dir) {
        $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
        try {
            $current = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
            $entries = @($current -split ';' | Where-Object { $_ -ne '' })
            foreach ($entry in $entries) {
                if ([Environment]::ExpandEnvironmentVariables($entry).TrimEnd('\') -ieq $Dir.TrimEnd('\')) {
                    return $false
                }
            }
            $kind = [Microsoft.Win32.RegistryValueKind]::ExpandString
            if ($key.GetValueNames() -contains 'Path') {
                $kind = $key.GetValueKind('Path')
            }
            $key.SetValue('Path', (($entries + $Dir) -join ';'), $kind)
        } finally {
            $key.Dispose()
        }
        # Setting a user variable broadcasts WM_SETTINGCHANGE, so terminals opened from now on
        # see the new PATH without signing out.
        [Environment]::SetEnvironmentVariable('SEPAY_INSTALL_PATH_REFRESH', '1', 'User')
        [Environment]::SetEnvironmentVariable('SEPAY_INSTALL_PATH_REFRESH', $null, 'User')
        return $true
    }

    if ($PSVersionTable.PSEdition -eq 'Core' -and -not $IsWindows) {
        throw 'this installer is for Windows; on macOS or Linux run: curl -fsSL https://get.sepay.vn/sepay-cli-install.sh | sh'
    }

    $arch = $env:PROCESSOR_ARCHITEW6432
    if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
    switch ($arch) {
        'AMD64' { $arch = 'amd64' }
        'ARM64' { $arch = 'arm64' }
        default { throw "unsupported architecture $arch; supported: AMD64, ARM64" }
    }

    $requireSignature = $env:SEPAY_REQUIRE_SIGNATURE -and $env:SEPAY_REQUIRE_SIGNATURE -ne '0'
    $cosign = Get-Command cosign -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($requireSignature -and -not $cosign) {
        throw 'SEPAY_REQUIRE_SIGNATURE is set but cosign is not on PATH; install cosign (https://docs.sigstore.dev) or unset it'
    }

    $dir = $env:SEPAY_INSTALL_DIR
    if (-not $dir) { $dir = Join-Path $env:LOCALAPPDATA 'Programs\sepay' }
    # Relative to the PowerShell location, not to the process directory GetFullPath would use.
    $dir = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($dir)

    # Process-wide settings: put them back, since under `iex` this is the user's own session.
    $savedProtocol = [Net.ServicePointManager]::SecurityProtocol
    $savedNotifier = $env:SEPAY_NO_UPDATE_NOTIFIER
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("sepay-install-" + [Guid]::NewGuid().ToString('N'))
    $client = $null
    try {
        # Windows PowerShell 5.1 may still default to TLS 1.0; GitHub requires 1.2.
        [Net.ServicePointManager]::SecurityProtocol = $savedProtocol -bor [Net.SecurityProtocolType]::Tls12
        Add-Type -AssemblyName System.Net.Http
        $handler = New-Object System.Net.Http.HttpClientHandler
        $handler.AllowAutoRedirect = $false
        $client = New-Object System.Net.Http.HttpClient($handler)
        $client.Timeout = [TimeSpan]::FromMinutes(5)
        $null = New-Item -ItemType Directory -Path $tmp

        $version = $env:SEPAY_VERSION
        if ($version) {
            $version = $version -replace '^v', ''
            if (-not (Test-Version $version)) {
                throw 'SEPAY_VERSION must look like 0.2.0 or 0.2.0-rc.1'
            }
        } else {
            # The latest release's own checksums.txt names its archives, so no API call (and no API
            # rate limit) is needed. It is only read for the version; the one verified below comes
            # from that tag.
            $latest = Join-Path $tmp 'latest.txt'
            try {
                Save-Url -Client $client -Url "https://github.com/$repo/releases/latest/download/checksums.txt" -Path $latest
            } catch {
                throw "could not find the latest release of $repo; set SEPAY_VERSION, e.g. 0.1.0 ($($_.Exception.Message))"
            }
            $version = ''
            foreach ($line in [IO.File]::ReadAllLines($latest)) {
                if ($line -match "^[0-9a-f]{64}\s+sepay_(.+)_windows_$arch\.zip$") {
                    $version = $Matches[1]
                    break
                }
            }
            if (-not (Test-Version $version)) {
                throw "could not read the latest release of $repo; set SEPAY_VERSION, e.g. 0.1.0"
            }
        }

        $base = "https://github.com/$repo/releases/download/v$version"
        $archive = "sepay_${version}_windows_$arch.zip"
        Write-Step "installing sepay $version (windows/$arch)"

        Save-Url -Client $client -Url "$base/$archive" -Path (Join-Path $tmp $archive)
        Save-Url -Client $client -Url "$base/checksums.txt" -Path (Join-Path $tmp 'checksums.txt')

        # The SHA-256 proves the archive matches checksums.txt; only the signature proves
        # checksums.txt came from our release workflow. Whoever can swap the archive can also
        # delete the signature, so with cosign present a missing signature stops the install.
        if ($cosign) {
            try {
                Save-Url -Client $client -Url "$base/checksums.txt.sig" -Path (Join-Path $tmp 'checksums.txt.sig')
                Save-Url -Client $client -Url "$base/checksums.txt.pem" -Path (Join-Path $tmp 'checksums.txt.pem')
            } catch {
                throw "release $version has no signature for checksums.txt; not installing"
            }
            # Under 'Stop', Windows PowerShell 5.1 turns redirected native stderr into a terminating error.
            # A cosign that fails to start leaves LASTEXITCODE as the session had it, possibly 0.
            $ErrorActionPreference = 'Continue'
            $global:LASTEXITCODE = 1
            & $cosign.Source verify-blob (Join-Path $tmp 'checksums.txt') `
                --signature (Join-Path $tmp 'checksums.txt.sig') `
                --certificate (Join-Path $tmp 'checksums.txt.pem') `
                --certificate-identity-regexp "^https://github\.com/$repo/\.github/workflows/release\.yml@refs/(heads/main|tags/v.*)$" `
                --certificate-oidc-issuer https://token.actions.githubusercontent.com *> $null
            $verified = $global:LASTEXITCODE -eq 0
            $ErrorActionPreference = 'Stop'
            if (-not $verified) {
                throw "checksums.txt is not signed by the release workflow of $repo; not installing. Report this to support@sepay.vn"
            }
            Write-Step 'signature of checksums.txt verified'
        } else {
            Write-Step 'warning: cosign not found, checking SHA-256 only'
        }

        $want = ''
        foreach ($line in [IO.File]::ReadAllLines((Join-Path $tmp 'checksums.txt'))) {
            $fields = $line -split '\s+'
            if ($fields.Count -eq 2 -and $fields[1] -ceq $archive) {
                $want = $fields[0]
                break
            }
        }
        if ($want -notmatch '^[0-9a-f]{64}$') {
            throw "$archive has no SHA-256 in checksums.txt; not installing"
        }
        $got = (Get-FileHash -LiteralPath (Join-Path $tmp $archive) -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($got -ne $want) {
            throw "checksum mismatch for $archive; not installing"
        }
        Write-Step 'SHA-256 verified'

        $unpacked = Join-Path $tmp 'unpacked'
        Expand-Archive -LiteralPath (Join-Path $tmp $archive) -DestinationPath $unpacked
        $new = Join-Path $unpacked 'sepay.exe'
        if (-not (Test-Path -LiteralPath $new -PathType Leaf)) {
            throw "$archive holds no sepay.exe"
        }
        $env:SEPAY_NO_UPDATE_NOTIFIER = '1'
        # The first line is the version; the second is its release-notes URL.
        $global:LASTEXITCODE = 1
        $banner = @(& $new version)[0]
        if ($global:LASTEXITCODE -ne 0 -or -not $banner) {
            throw 'the downloaded sepay.exe does not run on this machine'
        }
        # "sepay version X" alone, or followed by " (commit, date)".
        if ($banner -cne "sepay version $version" -and -not $banner.StartsWith("sepay version $version ", [StringComparison]::Ordinal)) {
            throw "the downloaded sepay.exe does not report version $version; not installing"
        }

        $existing = Get-Command sepay -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
        try {
            $null = New-Item -ItemType Directory -Path $dir -Force
        } catch {
            throw "cannot create $dir; set SEPAY_INSTALL_DIR to a directory you can write to"
        }
        $target = Join-Path $dir 'sepay.exe'
        # Copy next to the target, then rename, so no half-written sepay.exe is ever left.
        $staged = Join-Path $dir ".sepay.new.$PID.exe"
        try {
            Copy-Item -LiteralPath $new -Destination $staged -Force
        } catch {
            throw "cannot write to $dir; set SEPAY_INSTALL_DIR to a directory you can write to"
        }
        try {
            try {
                Move-Item -LiteralPath $staged -Destination $target -Force
            } catch {
                # A running sepay.exe cannot be replaced, but it can be renamed; the old file
                # is removed by the next install or update.
                $old = "$target.old"
                Remove-Item -LiteralPath $old -Force -ErrorAction SilentlyContinue
                Move-Item -LiteralPath $target -Destination $old -Force
                try {
                    Move-Item -LiteralPath $staged -Destination $target -Force
                } catch {
                    Move-Item -LiteralPath $old -Destination $target -Force -ErrorAction SilentlyContinue
                    throw
                }
            }
        } finally {
            Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue
        }

        Write-Step "installed $banner to $target"
        if ($existing -and $existing.Source -ne $target) {
            Write-Step "note: another sepay at $($existing.Source) comes first on your PATH; remove it or put $dir before it"
        }
        $inSession = @($env:Path -split ';' | Where-Object { $_.TrimEnd('\') -ieq $dir.TrimEnd('\') }).Count -gt 0
        if ($env:SEPAY_NO_MODIFY_PATH -and $env:SEPAY_NO_MODIFY_PATH -ne '0') {
            if (-not $inSession) {
                Write-Step "add $dir to your PATH to run sepay from any directory"
            }
        } else {
            # sepay.exe is installed by now, so a PATH failure is a warning, not the outcome.
            try {
                if (Add-UserPath $dir) {
                    if (-not $inSession) { $env:Path = "$env:Path;$dir" }
                    Write-Step "added $dir to your user PATH; terminals that were already open need a restart"
                }
            } catch {
                Write-Step "warning: could not add $dir to your user PATH ($($_.Exception.Message)); add it by hand"
            }
        }
        Write-Step 'next: sepay login'
    } finally {
        if ($null -ne $client) { $client.Dispose() }
        [Net.ServicePointManager]::SecurityProtocol = $savedProtocol
        $env:SEPAY_NO_UPDATE_NOTIFIER = $savedNotifier
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

try {
    Install-Sepay
} catch {
    [Console]::Error.WriteLine("sepay-install: error: $($_.Exception.Message)")
    # Run as a file (CI, `powershell -File`), fail the process; under `iex`, leave the session open.
    if ($PSCommandPath) { exit 1 }
}
