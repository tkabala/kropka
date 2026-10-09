# The terminal half of the demo: `kropka --lan` in the foreground, its QR
# code, and Ctrl+C. record.sh runs it in a container on its own network, so
# the folder is ~/Pictures/space and the LAN address a believable
# 192.168.1.42, alongside phone.mjs, which scans the code and browses.

# Raw PNG frames, not a video: record.sh copies them out of the container and
# composes them into the demo at 1:1, so the text never gets scaled.
Output "/tmp/frames/"

Set Shell bash
# Drawn at the size it shows up in the GIF. 14 px makes 9 x 19 px cells in
# the container, so this is 40 x 33 cells: the whole session fits.
Set FontSize 14
Set Width 388
Set Height 643
Set Padding 0
Set Theme "Catppuccin Mocha"
Set TypingSpeed 70ms
Set CursorBlink false
Set Framerate 15

Hide
Type@1ms `cd ~/Pictures/space && PS1='\[\e[1;35m\]\w\[\e[0m\] \[\e[2m\]$\[\e[0m\] '` Enter
Type@1ms `clear; echo ${EPOCHREALTIME/[.,]/} > ~/.t0` Enter
Show

Sleep 700ms
Type "kropka --lan --quiet"
Sleep 300ms
Enter
# phone.mjs scans the code and browses. It has to be done by then:
# record.sh checks.
Sleep 28500ms
Ctrl+C
Sleep 2s

Hide
Type@1ms `echo ${EPOCHREALTIME/[.,]/} > ~/.t1` Enter
Sleep 300ms
