<#
.SYNOPSIS
    Installs or updates ad-mute on the Luckfox board over SSH.

.DESCRIPTION
    The script copies the files of the luckfox folder to the board. Then
    luckfox/install.sh installs them on the board, restarts ad-mute, and
    shows the last lines of its log.

      ad-mute-arm32 or ad-mute-arm64   becomes /usr/bin/ad-mute
      ad-mute.conf                     becomes /etc/ad-mute.conf
      S99ad-mute                       starts ad-mute at boot on a board
                                       with init scripts
      ad-mute.service                  starts ad-mute at boot on a board
                                       with systemd

    Edit luckfox\ad-mute.conf before you deploy. The deploy replaces the
    settings file on the board.

    The deploy never changes /etc/ad-mute on the board. That folder holds
    the ADB key that the TV trusts, so the TV does not ask to allow the
    board again.

    The script uses ssh.exe and scp.exe from the OpenSSH client of Windows.
    It opens up to three SSH connections. Each connection asks for the
    board password, unless the board has your SSH key.

.PARAMETER Board
    SSH address of the board, such as root@192.168.1.60. A host name from
    your SSH config file also works.

.PARAMETER Arch
    The build of ad-mute to install: arm32 or arm64. If you omit it, the
    script asks the board for its machine type.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\Deploy-Luckfox.ps1 -Board root@192.168.1.60

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\Deploy-Luckfox.ps1 -Board root@192.168.1.60 -Arch arm32
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$Board,

    [ValidateSet('arm32', 'arm64')]
    [string]$Arch
)

$ErrorActionPreference = 'Stop'
$Source = Join-Path $PSScriptRoot 'luckfox'
$Stage  = 'ad-mute-deploy'   # name of the copy folder, on this PC and in /tmp on the board

foreach ($tool in 'ssh', 'scp') {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) {
        throw "$tool is not on the path. Add OpenSSH Client in Settings, System, Optional features."
    }
}

function Assert-ExitCode([string]$Step) {
    if ($LASTEXITCODE -ne 0) { throw "$Step failed with exit code $LASTEXITCODE." }
}

function Get-ScpClassicFlag {
    # From OpenSSH 9.0, scp copies over SFTP. The Dropbear SSH server of many
    # boards has no SFTP, so the -O option asks for the classic scp protocol.
    # Clients before OpenSSH 8.7 do not know -O, but they use the classic
    # protocol anyway.
    $saved = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { $usage = (& scp -O 2>&1) | Out-String }
    finally { $ErrorActionPreference = $saved }
    if ($usage -match 'unknown option|illegal option') { return @() }
    return @('-O')
}

if (-not $Arch) {
    Write-Host "Reading the machine type of $Board"
    $machine = "$(& ssh $Board 'uname -m')".Trim()
    Assert-ExitCode 'Reading the machine type'
    $Arch = switch -Regex ($machine) {
        '^(aarch64|arm64)$' { 'arm64'; break }
        '^arm'              { 'arm32'; break }
        default {
            throw "The board reports the machine type '$machine'. ad-mute has builds for arm32 and arm64 only. Use -Arch to choose one."
        }
    }
    Write-Host "Machine type: $machine, so the $Arch build"
}

$tempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('ad-mute-' + [guid]::NewGuid().ToString('N'))
$localStage = Join-Path $tempRoot $Stage
New-Item -ItemType Directory -Path $localStage | Out-Null

try {
    Copy-Item (Join-Path $Source "ad-mute-$Arch") (Join-Path $localStage 'ad-mute')
    foreach ($file in 'ad-mute.conf', 'S99ad-mute', 'ad-mute.service', 'install.sh') {
        Copy-Item (Join-Path $Source $file) $localStage
    }

    Write-Host "Copying the files to ${Board}:/tmp/$Stage"
    $classic = @(Get-ScpClassicFlag)
    # A relative path keeps scp from reading the drive letter as a host name.
    Push-Location $tempRoot
    try { & scp @classic -r $Stage "${Board}:/tmp/" }
    finally { Pop-Location }
    Assert-ExitCode 'Copying the files'

    Write-Host 'Installing ad-mute on the board'
    # The board removes its copy folder, whether the install works or not.
    $remote = "cd /tmp/$Stage && tr -d '\r' < install.sh > install.lf && sh install.lf; s=`$?; rm -rf /tmp/$Stage; exit `$s"
    & ssh $Board $remote
    Assert-ExitCode 'Installing'
}
finally {
    Remove-Item -Recurse -Force $tempRoot -ErrorAction SilentlyContinue
}

Write-Host "ad-mute ($Arch build) runs on $Board." -ForegroundColor Green
