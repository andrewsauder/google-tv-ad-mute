<#
.SYNOPSIS
    Captures screen, player and log data from a streaming app on an
    Android TV box, to find the marker the app gives during an ad.

.DESCRIPTION
    Start the script, start the app, and press a key at each moment:

      S   snapshot during the show
      A   snapshot during an ad
      P   snapshot while the show is paused
      Q   finish: save the log, write a summary, and zip the results

    Each snapshot saves these files:

      <app>-<kind><n>.xml         screen elements (uiautomator dump)
      <app>-<kind><n>-media.txt   full media session dump
      <app>-<kind><n>.png         screenshot (only with -Screenshot)

    The times.txt file lists each snapshot with the playback state,
    speed and position of the app.

    At the finish, the script also saves the full log and writes
    summary.txt. The summary compares each snapshot with the first show
    snapshot. It also lists log lines that often mark an ad break.

    Run the script in a PowerShell console window. The key presses do not
    work in the PowerShell ISE.

.PARAMETER App
    Short label for the app, such as tubi or paramount. The label starts
    each file name.

.PARAMETER Package
    Android package name of the app, such as com.cbs.ott. If you omit it,
    the script reads the app in the foreground at the first snapshot.

.PARAMETER Device
    The ip:port of the box, from the Wireless debugging screen. If you
    omit it, adb uses the one connected device.

.PARAMETER OutRoot
    Folder for the results. Each run gets its own subfolder and zip file.

.PARAMETER Screenshot
    Also saves a screenshot at each snapshot. Protected video shows as
    black, but app overlays such as an ad countdown still show.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\Capture-AdTest.ps1 -App tubi

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File .\Capture-AdTest.ps1 -App paramount -Package com.cbs.ott -Device 192.168.0.154:41234 -Screenshot
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern('^[\w-]+$')]
    [string]$App,

    [string]$Package,

    [string]$Device,

    [string]$OutRoot = (Join-Path $PSScriptRoot 'app-monitor'),

    [switch]$Screenshot
)

# ---------------------------------------------------------------------------
# adb helpers
# ---------------------------------------------------------------------------

$script:AdbBase = @()
if ($Device) { $script:AdbBase = @('-s', $Device) }

function Invoke-Adb {
    # Runs adb against the chosen device and returns every output line as a string.
    $out = & adb.exe @script:AdbBase @args 2>&1
    $out | ForEach-Object { "$_" }
}

function Get-ForegroundPackage {
    $text = Invoke-Adb shell dumpsys activity activities
    foreach ($line in $text) {
        if ($line -match 'ResumedActivity.*\su\d+\s([\w.]+)/') { return $Matches[1] }
    }
    return $null
}

function Get-PlaybackLine([string[]]$MediaText, [string]$Pkg) {
    # Returns the PlaybackState line of the first media session that the package owns.
    $pattern = '^\s*package=' + [regex]::Escape($Pkg) + '\s*$'
    for ($i = 0; $i -lt $MediaText.Count; $i++) {
        if ($MediaText[$i] -match $pattern) {
            $end = [Math]::Min($i + 20, $MediaText.Count)
            for ($j = $i + 1; $j -lt $end; $j++) {
                if ($MediaText[$j] -match 'state=PlaybackState') { return $MediaText[$j].Trim() }
            }
        }
    }
    return $null
}

function Format-Playback([string]$Line) {
    if (-not $Line) { return 'no media session for the package' }
    $state = if ($Line -match '\{state=(\w+)')   { $Matches[1] } else { '?' }
    $speed = if ($Line -match 'speed=([\d.]+)')  { $Matches[1] } else { '?' }
    $pos   = if ($Line -match 'position=(\d+)')  { $Matches[1] } else { '?' }
    return "state=$state speed=$speed position=$pos"
}

function Get-UiAttributes([string]$Path) {
    $pattern = '(text|resource-id|content-desc)="[^"]+"'
    @(Select-String -Path $Path -Pattern $pattern -AllMatches |
        ForEach-Object { $_.Matches.Value } |
        Sort-Object -Unique)
}

# ---------------------------------------------------------------------------
# Snapshot
# ---------------------------------------------------------------------------

function Save-Snapshot([string]$Kind) {
    $script:Counts[$Kind]++
    $label = '{0}-{1}{2}' -f $App, $Kind, $script:Counts[$Kind]
    $time  = Get-Date -Format 'HH:mm:ss'

    if (-not $script:Pkg) {
        $script:Pkg = Get-ForegroundPackage
        if ($script:Pkg) {
            Write-Host "Package in the foreground: $script:Pkg"
        }
        else {
            Write-Warning 'The foreground app is unknown. Restart the script with -Package.'
        }
    }

    # The media state comes first. It is fast, so its time best matches the key press.
    $media = @(Invoke-Adb shell dumpsys media_session)
    $media | Out-File (Join-Path $script:OutDir "$label-media.txt") -Encoding utf8
    $play = if ($script:Pkg) { Format-Playback (Get-PlaybackLine $media $script:Pkg) } else { 'package unknown' }

    # uiautomator often fails with "could not get idle state" while video plays,
    # so the script tries twice.
    $xmlOk = $false
    for ($try = 1; $try -le 2 -and -not $xmlOk; $try++) {
        $result = (Invoke-Adb shell uiautomator dump /sdcard/adtest-ui.xml) -join ' '
        if ($result -match 'dumped to') {
            Invoke-Adb pull /sdcard/adtest-ui.xml (Join-Path $script:OutDir "$label.xml") | Out-Null
            $xmlOk = $true
        }
    }

    if ($Screenshot) {
        Invoke-Adb shell screencap -p /sdcard/adtest-screen.png | Out-Null
        Invoke-Adb pull /sdcard/adtest-screen.png (Join-Path $script:OutDir "$label.png") | Out-Null
    }

    $note  = if ($xmlOk) { '' } else { '  (no xml: uiautomator failed)' }
    $entry = '{0}  {1,-20} {2}{3}' -f $time, $label, $play, $note
    Add-Content -Path $script:TimesFile -Value $entry -Encoding utf8

    $color = switch ($Kind) { 'ad' { 'Yellow' } 'paused' { 'Cyan' } default { 'Green' } }
    Write-Host $entry -ForegroundColor $color
}

# ---------------------------------------------------------------------------
# Finish: log, summary, zip
# ---------------------------------------------------------------------------

function Complete-Capture {
    Write-Host ''
    Write-Host 'Saving the log...'
    $logFile = Join-Path $script:OutDir "$App-log.txt"
    $log = @(Invoke-Adb logcat -d -v time)
    $log | Out-File $logFile -Encoding utf8

    $summary = [System.Collections.Generic.List[string]]::new()
    $summary.Add("App:      $App")
    $summary.Add("Package:  $script:Pkg")
    $summary.Add("Started:  $script:Started")
    $summary.Add("Finished: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')")
    $summary.Add('')
    $summary.Add('== Snapshots ==')
    if (Test-Path $script:TimesFile) {
        Get-Content $script:TimesFile | ForEach-Object { $summary.Add($_) }
    }

    # Screen elements: compare each snapshot with the first show snapshot.
    $summary.Add('')
    $summary.Add('== Screen elements compared with the first show snapshot ==')
    $baseFile = Join-Path $script:OutDir "$App-show1.xml"
    if (Test-Path $baseFile) {
        $base = Get-UiAttributes $baseFile
        $others = Get-ChildItem $script:OutDir -Filter "$App-*.xml" |
            Where-Object { $_.Name -ne "$App-show1.xml" } |
            Sort-Object LastWriteTime
        foreach ($file in $others) {
            $cur = Get-UiAttributes $file.FullName
            $added   = @($cur  | Where-Object { $_ -notin $base })
            $removed = @($base | Where-Object { $_ -notin $cur })
            $summary.Add("-- $($file.BaseName)")
            if ($added.Count -eq 0 -and $removed.Count -eq 0) {
                $summary.Add('   Identical. The app shows no ad element outside the video.')
            }
            $added   | ForEach-Object { $summary.Add("   + only here:    $_") }
            $removed | ForEach-Object { $summary.Add("   - only in show1: $_") }
        }
    }
    else {
        $summary.Add('No show1 xml file. Take an S snapshot first next time.')
    }

    # Log: codec and analytics events from the app process, plus ad SDK keywords anywhere.
    $summary.Add('')
    $summary.Add('== Log lines that often mark an ad break ==')
    $pidPattern = $null
    if ($script:Pkg) {
        $pids = ((Invoke-Adb shell pidof $script:Pkg) -join ' ').Trim() -split '\s+' |
            Where-Object { $_ -match '^\d+$' }
        if ($pids) {
            $pidPattern = '\(\s*(' + ($pids -join '|') + ')\):'
            $summary.Add("App process id: $($pids -join ', ')")
        }
        else {
            $summary.Add('The app process is not running, so the codec filter is off.')
        }
    }
    $codecPattern = 'allocate\(|Created component|AudioTrack.*stop\(|DrmUtils|Events:'
    $adPattern    = 'adbreak|ad_break|adpod|ad_pod|adstart|ad_start|adcomplete|ad_complete|freewheel|imasdk|AdEvent|ssai|onAdStarted|onAdEnded|CONTENT_PAUSE|CONTENT_RESUME'
    $hits = @($log | Where-Object {
        ($pidPattern -and $_ -match $pidPattern -and $_ -match $codecPattern) -or ($_ -match $adPattern)
    })
    if ($hits.Count -eq 0) {
        $summary.Add('No matching lines.')
    }
    else {
        $hits | Select-Object -First 300 | ForEach-Object { $summary.Add($_) }
        if ($hits.Count -gt 300) { $summary.Add("... $($hits.Count - 300) more lines in $App-log.txt") }
    }

    $summaryFile = Join-Path $script:OutDir 'summary.txt'
    $summary | Set-Content $summaryFile -Encoding utf8

    Invoke-Adb shell rm -f /sdcard/adtest-ui.xml /sdcard/adtest-screen.png | Out-Null

    $zip = "$script:OutDir.zip"
    Compress-Archive -Path (Join-Path $script:OutDir '*') -DestinationPath $zip -Force
    Write-Host ''
    Write-Host "Summary: $summaryFile" -ForegroundColor Green
    Write-Host "Zip:     $zip" -ForegroundColor Green
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

if (-not (Get-Command adb.exe -ErrorAction SilentlyContinue)) {
    throw 'adb.exe is not on the path. Install it with: winget install Google.PlatformTools'
}

if ($Device) { & adb.exe connect $Device | Out-Null }

$deviceState = "$((Invoke-Adb get-state) | Select-Object -Last 1)".Trim()
if ($deviceState -ne 'device') {
    throw "No device is ready. adb said: $deviceState. Run adb connect <ip>:<port>, or pass -Device."
}

$script:Pkg       = $Package
$script:Counts    = @{ show = 0; ad = 0; paused = 0 }
$script:Started   = Get-Date -Format 'yyyy-MM-dd HH:mm:ss'
$script:OutDir    = Join-Path $OutRoot ('{0}-{1}' -f $App, (Get-Date -Format 'yyyyMMdd-HHmm'))
$script:TimesFile = Join-Path $script:OutDir 'times.txt'
New-Item -ItemType Directory -Path $script:OutDir -Force | Out-Null

# A large log buffer holds a full ad break. The clear removes old lines.
Invoke-Adb logcat -G 16M | Out-Null
Invoke-Adb logcat -c | Out-Null

Write-Host "Results folder: $script:OutDir"
Write-Host ''
Write-Host 'Keys:  S = show   A = ad   P = paused   Q = finish' -ForegroundColor Cyan
Write-Host 'Order: S a minute into the show, A at the ad start, A at a later ad,'
Write-Host '       S after the show returns, P during a pause, then Q.'
Write-Host ''

try {
    :keys while ($true) {
        $key = [Console]::ReadKey($true)
        switch ($key.Key) {
            'S' { Save-Snapshot 'show' }
            'A' { Save-Snapshot 'ad' }
            'P' { Save-Snapshot 'paused' }
            'Q' { break keys }
        }
    }
}
finally {
    # This block also runs after Ctrl+C, so the log and the zip are never lost.
    Complete-Capture
}
