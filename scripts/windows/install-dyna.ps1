<#
.SYNOPSIS
Installs or removes the Dyna environment as one WSL2 distro.

.DESCRIPTION
Imports a trusted Dyna rootfs tar with wsl --import, sets the Dyna user ID and
ports, enables the dyna-engine user unit, and waits until the web app answers
on http://localhost:<Port>. A logon task keeps the distro running.
See docs/windows-install.md.

.EXAMPLE
powershell -ExecutionPolicy Bypass -File install-dyna.ps1 -Image C:\Downloads\dyna.tar.zst

.EXAMPLE
powershell -ExecutionPolicy Bypass -File install-dyna.ps1 -Uninstall
#>
[CmdletBinding()]
param(
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$')]
    [string]$Name = 'dyna',
    [string]$InstallDir = '',
    [string]$Image = '',
    [ValidateRange(1, 65535)][int]$Port = 7467,
    [ValidateRange(1, 65535)][int]$CardPort = 7468,
    # All WSL2 distros share one cgroup tree. Another distro's user 1000 would
    # clash with user@1000.service here, so Dyna uses its own user ID.
    [ValidateRange(1001, 59999)][int]$Uid = 7467,
    [switch]$NoAutoStart,
    [switch]$Uninstall
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
# wsl.exe writes UTF-16 unless WSL_UTF8 is set.
$env:WSL_UTF8 = '1'
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false
$OutputEncoding = New-Object System.Text.UTF8Encoding $false

if (-not $InstallDir) { $InstallDir = Join-Path $env:LOCALAPPDATA "Dyna\wsl\$Name" }
$TaskName = "Dyna WSL ($Name)"

function Step([string]$Text) { Write-Host "==> $Text" }

function Invoke-Wsl {
    # Windows PowerShell 5.1 turns native stderr into errors under 'Stop'.
    $ErrorActionPreference = 'Continue'
    $out = & wsl.exe @args 2>&1 | ForEach-Object { "$_" }
    $text = ($out -join "`n") -replace "`0", ''
    if ($LASTEXITCODE -ne 0) { throw "wsl.exe $($args -join ' ') failed ($LASTEXITCODE): $text" }
    return $text
}

# Runs a POSIX shell script as root inside the distro. Arguments must be plain words.
function Invoke-Root([string]$Script, [string[]]$Arguments = @()) {
    $ErrorActionPreference = 'Continue'
    $out = $Script | & wsl.exe -d $Name -u root --exec /bin/sh -c "tr -d '\r' | /bin/sh -s -- $($Arguments -join ' ')" 2>&1 | ForEach-Object { "$_" }
    $text = ($out -join "`n") -replace "`0", ''
    if ($LASTEXITCODE -ne 0) { throw "Command in $Name failed ($LASTEXITCODE): $text" }
    return $text
}

function Get-Distros {
    $ErrorActionPreference = 'Continue'
    $text = & wsl.exe --list --quiet 2>$null
    if ($LASTEXITCODE -ne 0) { return @() }
    return @($text | ForEach-Object { ("$_" -replace "`0", '').Trim() } | Where-Object { $_ })
}

function Test-WslVersion {
    $ErrorActionPreference = 'Continue'
    $text = & wsl.exe --version 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw 'This needs WSL 2 from the Microsoft Store. Run "wsl --install --no-distribution" or "wsl --update", then try again.'
    }
    $text = ($text | ForEach-Object { "$_" }) -join "`n" -replace "`0", ''
    if ($text -notmatch 'WSL[^:\r\n]*:\s*(\d+)\.(\d+)') { throw "Cannot read the WSL version: $text" }
    if ([int]$Matches[1] -lt 2) { throw "WSL $($Matches[1]).$($Matches[2]) is too old. Run ""wsl --update""." }
    Step "WSL $($Matches[1]).$($Matches[2]) found"
}

function Test-PortFree([int]$Number) {
    $busy = Get-NetTCPConnection -State Listen -LocalPort $Number -ErrorAction SilentlyContinue
    if ($busy) { throw "Port $Number is in use on Windows. Choose another -Port or -CardPort." }
}

function Remove-KeepAlive {
    $task = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($task) {
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
        Step "Removed scheduled task '$TaskName'"
    }
}

# WSL stops an idle distro a few seconds after its last Windows client exits,
# even with systemd. A sleep process keeps dyna-engine running. conhost
# --headless hides its console, also when Windows Terminal is the default.
function Start-KeepAlive {
    $exe = Join-Path $env:SystemRoot 'System32\conhost.exe'
    $arguments = "--headless wsl.exe -d $Name --exec /bin/sleep infinity"
    Start-Process -FilePath $exe -ArgumentList $arguments -WindowStyle Hidden
    if ($NoAutoStart) {
        Step 'Started a keepalive process for this logon only'
        return
    }
    $action = New-ScheduledTaskAction -Execute $exe -Argument $arguments
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User ([Security.Principal.WindowsIdentity]::GetCurrent().Name)
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero)
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Settings $settings `
        -Description "Keeps the Dyna WSL distro '$Name' running." | Out-Null
    Step "Started a keepalive process and registered logon task '$TaskName'"
}

function Get-ImageFile {
    if ($Image -match '^[A-Za-z][A-Za-z0-9+.-]*://') {
        $uri = [Uri]$Image
        if ($uri.Scheme -ne 'https' -or $uri.UserInfo) { throw 'The image URL must use HTTPS without user information.' }
        $leaf = [IO.Path]::GetFileName($uri.AbsolutePath)
        if ($leaf -notmatch '\.(tar|tar\.zst|tar\.gz|tar\.xz)$') { $leaf = 'dyna.tar.zst' }
        $file = Join-Path $InstallDir "download-$leaf"
        Step "Downloading $Image"
        & curl.exe --fail --location --proto '=https' --proto-redir '=https' --silent --show-error --output $file $Image
        if ($LASTEXITCODE -ne 0) { throw "Download failed ($LASTEXITCODE)." }
        return @{ Path = $file; Temporary = $true }
    }
    $path = (Resolve-Path -LiteralPath $Image).Path
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "$Image is not a file." }
    return @{ Path = $path; Temporary = $false }
}

$configure = @'
set -eu
uid=$1 port=$2 card=$3
test -x /usr/local/bin/dyna
test -x /usr/local/bin/dyna-desktop
test -f /etc/dyna/image-release
test -f /etc/systemd/user/dyna-engine.service
test "$(cat /proc/1/comm)" = systemd || { echo "systemd is not PID 1; check /etc/wsl.conf" >&2; exit 1; }
if [ -e "/sys/fs/cgroup/user.slice/user-$uid.slice" ]; then
  echo "User ID $uid is active in another WSL distro. Choose another -Uid." >&2; exit 1
fi
for p in "$port" "$card"; do
  if ss -Hltn "sport = :$p" | grep -q .; then echo "Port $p is in use inside WSL. Choose another port." >&2; exit 1; fi
done
old_uid=$(id -u dyna)
old_gid=$(id -g dyna)
if [ "$old_gid" != "$uid" ]; then groupmod -g "$uid" dyna; fi
if [ "$old_uid" != "$uid" ]; then usermod -u "$uid" dyna; fi
usermod -g "$uid" dyna
chown -R "$uid:$uid" /home/dyna
mkdir -p /etc/dyna /var/lib/systemd/linger "/etc/systemd/system/user-$uid.slice.d"
printf 'DYNA_SERVE_PORT=%s\nDYNA_CARD_PORT=%s\n' "$port" "$card" > /etc/dyna/serve.env
# All WSL2 distros share the VM. Keep Dyna below other work, as the nspawn install does.
printf '[Slice]\nCPUWeight=20\nMemoryHigh=65%%\nMemoryMax=80%%\n' > "/etc/systemd/system/user-$uid.slice.d/dyna.conf"
mkdir -p /home/dyna/.config/systemd/user/default.target.wants
ln -sf /etc/systemd/user/dyna-engine.service /home/dyna/.config/systemd/user/default.target.wants/dyna-engine.service
chown -R "$uid:$uid" /home/dyna/.config
touch /var/lib/systemd/linger/dyna
echo configured
'@

function Install-Dyna {
    if (-not $Image) { throw 'Pass -Image with a trusted dyna.tar, dyna.tar.xz, dyna.tar.zst, dyna.tar.gz or an HTTPS URL.' }
    if ($Port -eq $CardPort) { throw '-Port and -CardPort must differ.' }
    Test-WslVersion
    if ((Get-Distros) -contains $Name) { throw "A WSL distro named '$Name' already exists. Choose another -Name or uninstall it first." }
    if ((Test-Path $InstallDir) -and (Get-ChildItem -Force $InstallDir | Select-Object -First 1)) {
        throw "$InstallDir is not empty. Choose another -InstallDir."
    }
    Test-PortFree $Port
    Test-PortFree $CardPort
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

    $clock = [Diagnostics.Stopwatch]::StartNew()
    $source = Get-ImageFile
    try {
        Step "Importing $($source.Path) as '$Name' into $InstallDir"
        try {
            Invoke-Wsl --import $Name $InstallDir $source.Path --version 2 | Out-Null
        } catch {
            throw "Import failed. Run ""wsl --update"" or pass an uncompressed dyna.tar. $($_.Exception.Message)"
        }
    } finally {
        if ($source.Temporary) { Remove-Item -Force -LiteralPath $source.Path -ErrorAction SilentlyContinue }
    }
    $imported = $clock.Elapsed.TotalSeconds
    try {
        Step 'Waiting for systemd'
        Invoke-Root 'timeout 120 systemctl is-system-running --wait || true' | Out-Null
        Step "Setting user ID $Uid and ports $Port and $CardPort"
        Invoke-Root $configure @("$Uid", "$Port", "$CardPort") | Out-Null
        Step 'Restarting the distro'
        Invoke-Wsl --terminate $Name | Out-Null
        $start = [Diagnostics.Stopwatch]::StartNew()
        Start-KeepAlive
        Step "Waiting for http://localhost:$Port"
        $ok = $false
        while ($start.Elapsed.TotalSeconds -lt 180) {
            try {
                $r = Invoke-WebRequest -UseBasicParsing -TimeoutSec 3 "http://localhost:$Port/"
                if ($r.StatusCode -eq 200) { $ok = $true; break }
            } catch { }
            Start-Sleep -Milliseconds 500
        }
        if (-not $ok) {
            throw "No HTTP 200 from http://localhost:$Port after 180 seconds. Check: wsl -d $Name -- journalctl --user -u dyna-engine.service"
        }
    } catch {
        Write-Warning "Partial install. Remove it with: install-dyna.ps1 -Uninstall -Name $Name"
        throw
    }
    $ready = $start.Elapsed.TotalSeconds
    Write-Host ''
    Write-Host ('Dyna is running. Import took {0:N1} s; start to HTTP 200 took {1:N1} s.' -f $imported, $ready)
    Write-Host "Open http://localhost:$Port in your browser."
    Write-Host 'Get a pairing code:'
    Write-Host "  wsl -d $Name -- dyna engine run --pair"
    Write-Host 'Add agent profiles after you log in to an agent tool:'
    Write-Host "  wsl -d $Name -- dyna profiles add --name my-builder --harness codex --model <model-id> --taste 7 --intelligence 9 --cost 6"
}

function Uninstall-Dyna {
    if (-not ((Get-Distros) -contains $Name)) {
        Remove-KeepAlive
        throw "No WSL distro named '$Name'."
    }
    $ErrorActionPreference = 'Continue'
    $marker = & wsl.exe -d $Name -u root --exec /bin/sh -c 'test -f /etc/dyna/image-release && test -x /usr/local/bin/dyna-desktop && echo dyna-image' 2>$null
    if ((($marker | ForEach-Object { "$_" }) -join '' -replace "`0", '') -notmatch 'dyna-image') {
        throw "Refusing to remove '$Name': it has no Dyna marker (/etc/dyna/image-release)."
    }
    $ErrorActionPreference = 'Stop'
    Remove-KeepAlive
    Step "Unregistering '$Name'. This deletes all data inside it."
    Invoke-Wsl --unregister $Name | Out-Null
    if ((Test-Path $InstallDir) -and -not (Get-ChildItem -Force $InstallDir | Select-Object -First 1)) {
        Remove-Item -Force $InstallDir
    }
    Write-Host "Removed the Dyna distro '$Name'."
}

if ($Uninstall) { Uninstall-Dyna } else { Install-Dyna }
