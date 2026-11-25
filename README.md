# google-tv-muter

ad-mute mutes the TV during ad breaks in streaming apps on an Onn Google TV box. It runs on a Luckfox board on the same network. It talks to the box over ADB (Android Debug Bridge, the Android remote control and debug protocol). When an ad starts, ad-mute sends one mute key press. When the show returns, it sends one more.

| App | Package name (Android app ID) | How ad-mute finds an ad |
| --- | --- | --- |
| Paramount+ | `com.cbs.ott` | The media session of the app reports state `PLAYING` with speed `0.0`. |
| Pluto TV | `tv.pluto.android` | The app log marks the start and the end of each ad break. |
| YouTube | `com.google.android.youtube.tv` | The media session shows no ad. While YouTube plays, ad-mute captures the screen and looks for the word **Sponsored** in the ad overlay. |
| Tubi | `com.tubitv` | The media session shows no ad. While Tubi plays, ad-mute captures the screen and looks for the words **This ad will end in** in the ad label at the top left. |
| Google TV Freeplay | `com.google.android.apps.tv.launcherx` | Freeplay channels play in the Google TV home screen app, which has no media session. While the home screen app holds the audio focus, ad-mute captures the screen and looks for the countdown ring of the ad break at the top right. |

## Files

- `luckfox/`: the ad-mute program for the board, its settings file, and its start scripts. `luckfox/src` holds the Go source.
- `Deploy-Luckfox.ps1`: installs or updates ad-mute on the board.
- `ad-mute.ps1`: the same muting from a Windows PC, for tests. It does not check YouTube, Tubi or Google TV Freeplay.
- `Capture-AdTest.ps1`: records the screen, media session and log of an app, to find the ad marker of a new app.

## Deploy to the Luckfox board

### Before you start

- **PC:** Windows 10 or later with the OpenSSH client. If `ssh` does not run, add **OpenSSH Client** in **Settings** > **System** > **Optional features**.
- **Board:** The Luckfox board is on your network, and you can log in to it over SSH (secure shell) as `root`.
- **Onn box:** Debugging over the network is on in **Developer options**. You know the IP address and port of the box.

### Steps

1. Open `luckfox\ad-mute.conf` in a text editor.
2. Set `ONN_TARGET` to the IP address and port of the Onn box, such as `"192.168.1.154:5555"`.
3. Optional: change `PACKAGE`. It lists the apps to watch, separated by commas.
4. Save the file.
5. Open a PowerShell window in the repository folder.
6. Run the deploy script with the SSH address of the board:

   ```powershell
   powershell -ExecutionPolicy Bypass -File .\Deploy-Luckfox.ps1 -Board root@192.168.1.60
   ```

7. Enter the board password at each prompt. The script makes up to three SSH connections.
8. On the first deploy, look at the TV. On the prompt, tick **Always allow** and select **Allow**. You have 90 seconds.
9. Read the end of the output. It shows the last log lines and `ad-mute (arm32 build) runs on root@192.168.1.60.`

The deploy replaces the program, the settings file and the start script on the board. It then restarts ad-mute. It never changes `/etc/ad-mute` on the board. That folder holds the ADB key that the TV trusts, so the TV does not ask again on later deploys.

The script asks the board for its machine type. To skip that SSH connection, add `-Arch arm32` or `-Arch arm64`.

### Deploy a change

- **A settings change:** Edit `luckfox\ad-mute.conf`, and run the deploy script again.
- **A source change:** Rebuild both programs, and then run the deploy script again. Run these commands in PowerShell from the repository folder:

  ```powershell
  cd luckfox\src
  $env:CGO_ENABLED = '0'; $env:GOOS = 'linux'
  $env:GOARCH = 'arm'; $env:GOARM = '7'
  go build -trimpath -ldflags="-s -w" -o ..\ad-mute-arm32 .
  $env:GOARCH = 'arm64'
  go build -trimpath -ldflags="-s -w" -o ..\ad-mute-arm64 .
  ```

  These variables stay set until you close the PowerShell window.

## Check that ad-mute works

### Read the log

ad-mute writes one line for each event, such as an ad start or a lost connection. The log location depends on how the board starts programs.

On a board with init scripts, such as the Luckfox Buildroot image, the log is `/tmp/ad-mute.log`. Most boards keep `/tmp` in memory, so a board restart can clear the log. To follow the log live, run:

```powershell
ssh -t root@192.168.1.60 tail -f /tmp/ad-mute.log
```

On a board with systemd, run:

```powershell
ssh -t root@192.168.1.60 journalctl -u ad-mute -f
```

Press **Ctrl+C** to close the log view. ad-mute keeps running.

To check that ad-mute runs, run this command. A number means ad-mute runs. No output means it does not run.

```powershell
ssh root@192.168.1.60 pidof ad-mute
```

To restart ad-mute, run `/etc/init.d/S99ad-mute restart` on the board, or `systemctl restart ad-mute` under systemd.

### A normal log

```text
--- Deployed Wed Nov 25 20:14:58 UTC 2026
2026/11/25 20:15:02 Watching com.cbs.ott, tv.pluto.android, com.google.android.youtube.tv, com.tubitv, com.google.android.apps.tv.launcherx on 192.168.1.154:5555
2026/11/25 20:15:02 Connected to 192.168.1.154:5555
2026/11/25 20:29:50 Ad started in tv.pluto.android: muted
2026/11/25 20:32:20 Show resumed: unmuted
```

- The deploy script writes the `--- Deployed` line.
- During a show, the log stays quiet. ad-mute checks the apps every 500 ms, but it writes nothing until something changes.
- The `Ad started` line comes about 1 second after the ad break starts.
- The `Show resumed` line comes about 1 second after the ad break ends.

### Log messages

| Message | Meaning | What to do |
| --- | --- | --- |
| `Watching <apps> on <address>` | ad-mute started. | Nothing. |
| `Connected to <address>` | ad-mute reached the Onn box. | Nothing. |
| `Ad started in <app>: muted` | ad-mute found an ad and sent the mute key. | Nothing. |
| `Show resumed: unmuted` | The ad break ended, and ad-mute sent the mute key again. | Nothing. |
| `Stopped while muted: unmuted.` | ad-mute stopped during an ad, and it unmuted the TV first. | Nothing. |
| `Creating new ADB key at /etc/ad-mute/adbkey` | ad-mute has no key yet, so the TV must allow the board. | Watch the TV for the prompt. |
| `TV doesn't know this device yet. ...` | The TV shows a prompt to allow the board. ad-mute waits 90 seconds. | On the TV, tick **Always allow** and select **Allow**. |
| `Can't reach Onn box: not authorised on the TV ...` | Nobody allowed the board within 90 seconds. ad-mute tries again after 5 seconds. | Allow the board on the next prompt. |
| `Can't reach Onn box: dial tcp ...: connect: connection refused` | The box answers, but no ADB service listens on that port. The port changed, or debugging is off. | Check the IP address and port on the box. Update `ONN_TARGET`, and deploy again. |
| `Can't reach Onn box: dial tcp ...: i/o timeout` | Nothing answers at that address. The box is off, or its IP address changed. | Turn on the box, or update `ONN_TARGET` and deploy again. |
| `Can't reach Onn box: unexpected ADB reply 534c5453 during connect` | The box asks for an encrypted (TLS) connection. ad-mute supports only plain ADB. | See [Fix an encrypted connection](#fix-an-encrypted-connection). |
| `Lost connection: ...` | The connection to the box dropped. ad-mute connects again at the next check. | Nothing, unless the line repeats. Then look for a `Can't reach` line. |
| `Can't read the screen capture, so ads in <app> stay unmuted: ...` | The box sent a screen capture that ad-mute cannot decode. ad-mute counts it as no ad. It writes the line again only after a good capture. | Check the screen capture of the app. See [Check what ad-mute sees](#check-what-ad-mute-sees). |
| `ad-mute: -target is required (Onn box IP:port)` | `ONN_TARGET` in `/etc/ad-mute.conf` is empty. | Set `ONN_TARGET`, and deploy again. |
| `Watching ...` repeats every few seconds | ad-mute stops right after it starts, and the start script starts it again. | Read the line before each `Watching` line for the cause. |

### Fix an encrypted connection

The message `unexpected ADB reply 534c5453` means the box sent `STLS`, a request for an encrypted connection. The **Wireless debugging** port does this. Switch the box to plain ADB over the network:

1. On the PC, connect adb.exe to the box with `adb connect <box IP>:<port>`.
2. Run `adb tcpip 5555`.
3. Set `ONN_TARGET` to `<box IP>:5555`.
4. Deploy again.

The box can go back to its old mode after a restart. Nobody tested this fix with ad-mute yet.

## When ad-mute misses an ad or stays muted

### An ad plays with sound

1. Look for `Can't reach` or `Lost connection` lines in the log. Fix the connection first.
2. Check the `Watching` line. It must list the app.
3. If the log shows no `Ad started` line, check what ad-mute sees during the ad. See [Check what ad-mute sees](#check-what-ad-mute-sees).

### The TV stays muted after the ad break

- **The log shows `Show resumed: unmuted`:** The mute key is a toggle, and the toggle is out of step. This happens when you press the mute key yourself while ad-mute has the TV muted. Press the mute key once.
- **The log shows no `Show resumed` line:** ad-mute still sees an ad. In Pluto TV, one ad log line keeps the ad state for up to 35 seconds. This covers an app that closes during an ad. If the TV stays muted longer, check what ad-mute sees.

### The sound cuts out during the show

If the log shows an `Ad started` line during the show, ad-mute read the show as an ad. Check what ad-mute sees.

### Check what ad-mute sees

Run these commands on the PC while the app plays. adb.exe must be connected to the box, with `adb connect <box IP>:<port>`.

**Paramount+:**

```powershell
adb shell "dumpsys media_session | grep -A20 'package=com.cbs.ott' | grep -m1 'state=PlaybackState'"
```

During an ad, the line shows `state=PLAYING(3)` and `speed=0.0`. During the show, the speed is `1.0`.

**Pluto TV:**

```powershell
adb shell "logcat -d -b main -v time | grep -E 'slotStart|slotEnd|OnAdPodComplete|consumeID3' | tail -n 5"
```

- During an ad, the last line is a `consumeID3` or `slotStart` line from the last few seconds.
- After the ad break, the last line is a `slotEnd` line.
- If no lines show up during an ad, the app no longer writes these lines. A new app version can make this change. Make a new capture with `Capture-AdTest.ps1`, and look for a new ad marker.

**YouTube:**

```powershell
adb shell "dumpsys media_session | grep -A20 'package=com.google.android.youtube.tv' | grep -m1 'state=PlaybackState'"
adb shell screencap -p /sdcard/ad-mute-screen.png
adb pull /sdcard/ad-mute-screen.png
```

- The first command must show `state=PLAYING(3)`, during the show and during an ad. ad-mute captures the screen only then.
- Open `ad-mute-screen.png`. The video is not part of a screen capture, so the screen of a show is blank. During an ad, the bottom left shows the advertiser and the word **Sponsored**.
- If an ad shows no **Sponsored** word, or shows it in another place or language, ad-mute misses the ad. A new app version can make this change. Make a new capture with `Capture-AdTest.ps1 -Screenshot`.

**Tubi:**

```powershell
adb shell "dumpsys media_session | grep -A20 'package=com.tubitv' | grep -m1 'state=PlaybackState'"
adb shell screencap -p /sdcard/ad-mute-screen.png
adb pull /sdcard/ad-mute-screen.png
```

- The first command must show `state=PLAYING(3)`, during the show and during an ad. ad-mute captures the screen only then.
- Open `ad-mute-screen.png`. The video is not part of a screen capture, so the screen of a show is blank, apart from a short rating label. During an ad, the top left shows a gray label, such as **Ad 1 of 3. This ad will end in 00:05**.
- If an ad shows no such label, or shows other words, ad-mute misses the ad. A new app version can make this change. Make a new capture with `Capture-AdTest.ps1 -Screenshot`.
- While the show is paused, Tubi can show a still ad with **Press any button to close the ad**. No sound plays then, so ad-mute does not mute.

**Google TV Freeplay:**

```powershell
adb shell "dumpsys audio | grep -A10 'Audio Focus stack entries' | grep ' -- pack: '"
adb shell screencap -p /sdcard/ad-mute-screen.png
adb pull /sdcard/ad-mute-screen.png
```

- The first command lists the apps that asked for the audio focus. The last line is the app that holds it. While a channel plays, that line must show `pack: com.google.android.apps.tv.launcherx`. ad-mute captures the screen only then.
- If the first command shows no lines, or another app, while a channel plays, ad-mute misses Freeplay ads. Save the output of `adb shell dumpsys audio` while a channel plays. A fix of the check needs this output.
- Open `ad-mute-screen.png`. The video is not part of a screen capture, so the screen of a show is blank. During an ad break, the top right shows a ring with the time left in the break, such as **1:37**.
- If an ad break shows no ring, or shows it in another place, ad-mute misses the ad. A new version of the home screen app can make this change. Make a new capture with `Capture-AdTest.ps1 -Screenshot`.

These Freeplay cases have limits:

- The ring hides while the channel banner or the player controls show. The banner shows for a few seconds after each channel change. ad-mute unmutes while the ring hides, and mutes again when the ring comes back.
- In the channel guide, the channel preview can play an ad with sound. The capture has no guide screen from an ad break, so it does not show if the guide has the ring.
- Nobody tested the Freeplay check on the box yet. The audio focus command, and the time the ring takes to show and to hide at the start and the end of a break, are not confirmed.

### Test from the PC

`ad-mute.ps1` does the same checks from the PC and prints each event in its window. It does not check YouTube, Tubi or Google TV Freeplay. Stop ad-mute on the board first. Two copies send two mute key presses, which cancel each other out.

```powershell
ssh root@192.168.1.60 /etc/init.d/S99ad-mute stop
powershell -ExecutionPolicy Bypass -File .\ad-mute.ps1
```

Under systemd, stop the board copy with `systemctl stop ad-mute`.
