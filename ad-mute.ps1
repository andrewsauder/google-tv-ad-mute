<#
.SYNOPSIS
    Mutes the TV audio during ad breaks in a streaming app on the Onn box.

.DESCRIPTION
    Polls the ad state of each app over ADB. The script sends one mute key
    press when an ad starts and one more when the show returns. The mute key
    is a toggle, so the script tracks the mute state itself.

    Each app shows an ad in a different way:

      Paramount+ (com.cbs.ott)     The media session reports state PLAYING
                                   with playback speed 0.0.
      Pluto TV (tv.pluto.android)  The media session does not change. The
                                   app log marks the start and the end of
                                   each ad break, and an ID3 tag every few
                                   seconds during each ad.

    Press Ctrl+C to stop. If the audio is muted at that moment, the script
    unmutes it before it exits.

.PARAMETER Package
    Android package names of the apps to watch. Paramount+ is com.cbs.ott.
    Pluto TV is tv.pluto.android.

.PARAMETER UnmuteAfter
    Number of consecutive non-ad readings before the script unmutes.
    A value above 1 guards against one bad reading mid-ad.

.PARAMETER IntervalMs
    Delay between readings, in milliseconds.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\ad-mute.ps1

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\ad-mute.ps1 -Package tv.pluto.android
#>
param(
    [string[]]$Package = @('com.cbs.ott', 'tv.pluto.android'),
    [int]$UnmuteAfter = 2,
    [int]$IntervalMs = 500
)

# Pluto TV log lines. The log tag is an obfuscated class name, so only the text counts.
$PlutoPackage    = 'tv.pluto.android'
$PlutoBreakStart = 'Emitting an analytic event: slotStart'
$PlutoAdTag      = 'consumeID3: ID3Tag('
$PlutoPodDone    = 'Emitting an analytic event: OnAdPodComplete'
$PlutoBreakEnd   = 'Emitting an analytic event: slotEnd'

# One ad line keeps the ad state this long when no newer line comes. The
# longest gap between ad lines in the capture was 7.6 seconds.
$PlutoAdHoldSec = 35

# Prints "now=<epoch seconds>" and then the Pluto TV ad lines of the last 40
# seconds, each led by its epoch time. Prints nothing when the app is not running.
# The text has no double quotes, so Windows PowerShell passes it to adb unchanged.
$PlutoQuery = 'p=$(pidof -s tv.pluto.android) || exit 0; n=$(date +%s); echo now=$n; ' +
    'logcat -d -b main -v epoch --pid=$p -t $((n-40)).000 ' +
    '-e ''consumeID3: ID3Tag|Emitting an analytic event: (slotStart|slotEnd|OnAdPodComplete)'''

function Send-MuteToggle {
    adb shell input keyevent KEYCODE_VOLUME_MUTE | Out-Null
}

function Test-AdPlaying([string]$Line) {
    if (-not $Line) { return $false }
    return ($Line -match '\{state=PLAYING') -and ($Line -match 'speed=0\.0')
}

function Test-PlutoAd([string[]]$Lines) {
    # The last ad line decides. An end line means the show plays. A start
    # line or an ID3 line means an ad plays, unless that line is too old.
    $inv    = [System.Globalization.CultureInfo]::InvariantCulture
    $num    = [System.Globalization.NumberStyles]::Float
    $now    = -1.0
    $lastAd = 0.0
    $inAd   = $false
    foreach ($raw in $Lines) {
        $line = "$raw".Trim()
        $value = 0.0
        if ($line -match '^now=(\d+)$') {
            $now = [double]::Parse($Matches[1], $inv)
            continue
        }
        $first = ($line -split '\s+', 2)[0]
        if (-not [double]::TryParse($first, $num, $inv, [ref]$value)) { continue }
        if ($line.Contains($PlutoBreakEnd) -or $line.Contains($PlutoPodDone)) {
            $inAd = $false
        }
        elseif ($line.Contains($PlutoBreakStart) -or $line.Contains($PlutoAdTag)) {
            $inAd = $true
            $lastAd = $value
        }
    }
    return ($now -gt 0) -and $inAd -and (($now - $lastAd) -le $PlutoAdHoldSec)
}

function Get-AdPackage {
    # Returns the package of the first app that plays an ad, or $null if none does.
    foreach ($pkg in $Package) {
        if ($pkg -eq $PlutoPackage) {
            $lines = @(adb shell $PlutoQuery)
            if (Test-PlutoAd $lines) { return $pkg }
        }
        else {
            $line = adb shell "dumpsys media_session | grep -A20 'package=$pkg' | grep -m1 'state=PlaybackState'"
            if (Test-AdPlaying $line) { return $pkg }
        }
    }
    return $null
}

$muted = $false
$showCount = 0

Write-Host "Watching $($Package -join ', '). Press Ctrl+C to stop."

try {
    while ($true) {
        $adPackage = Get-AdPackage
        $time = Get-Date -Format 'HH:mm:ss'

        if ($adPackage) {
            $showCount = 0
            if (-not $muted) {
                Send-MuteToggle
                $muted = $true
                Write-Host "$time  Ad started in ${adPackage}: muted" -ForegroundColor Yellow
            }
        }
        elseif ($muted) {
            $showCount++
            if ($showCount -ge $UnmuteAfter) {
                Send-MuteToggle
                $muted = $false
                $showCount = 0
                Write-Host "$time  Show resumed: unmuted" -ForegroundColor Green
            }
        }

        Start-Sleep -Milliseconds $IntervalMs
    }
}
finally {
    if ($muted) {
        Send-MuteToggle
        Write-Host 'Stopped while muted: unmuted.'
    }
}
