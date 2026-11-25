<#
.SYNOPSIS
    Mutes the TV audio during ad breaks in a streaming app on the Onn box.

.DESCRIPTION
    Polls the media session of one app over ADB. During an ad, Paramount+
    reports state PLAYING with playback speed 0.0. The script sends one
    mute key press when an ad starts and one more when the show returns.
    The mute key is a toggle, so the script tracks the mute state itself.
    Press Ctrl+C to stop. If the audio is muted at that moment, the script
    unmutes it before it exits.

.PARAMETER Package
    Android package name of the app to watch. Paramount+ is com.cbs.ott.

.PARAMETER UnmuteAfter
    Number of consecutive non-ad readings before the script unmutes.
    A value above 1 guards against one bad reading mid-ad.

.PARAMETER IntervalMs
    Delay between readings, in milliseconds.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\ad-mute.ps1

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\ad-mute.ps1 -Package com.tubitv
#>
param(
    [string]$Package = 'com.cbs.ott',
    [int]$UnmuteAfter = 2,
    [int]$IntervalMs = 500
)

function Send-MuteToggle {
    adb shell input keyevent KEYCODE_VOLUME_MUTE | Out-Null
}

function Test-AdPlaying([string]$Line) {
    if (-not $Line) { return $false }
    return ($Line -match '\{state=PLAYING') -and ($Line -match 'speed=0\.0')
}

$muted = $false
$showCount = 0

Write-Host "Watching $Package. Press Ctrl+C to stop."

try {
    while ($true) {
        $line = adb shell "dumpsys media_session | grep -A20 'package=$Package' | grep -m1 'state=PlaybackState'"
        $time = Get-Date -Format 'HH:mm:ss'

        if (Test-AdPlaying $line) {
            $showCount = 0
            if (-not $muted) {
                Send-MuteToggle
                $muted = $true
                Write-Host "$time  Ad started: muted" -ForegroundColor Yellow
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
