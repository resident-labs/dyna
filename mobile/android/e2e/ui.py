#!/usr/bin/env python3
"""Taps and types in the app through uiautomator, so the test needs no coordinates.

  ui.py tap "Join call"            tap the node whose text matches
  ui.py type N "text"              type into the Nth EditText (0 based)
"""
import re, subprocess, sys, time, xml.etree.ElementTree as ET


def adb(*a):
    return subprocess.run(["adb", *a], check=True, capture_output=True, text=True).stdout


def nodes():
    try:
        adb("shell", "uiautomator", "dump", "/sdcard/ui.xml")
    except subprocess.CalledProcessError:
        return []  # the system is busy starting the app; the caller tries again
    try:
        return list(ET.fromstring(adb("exec-out", "cat", "/sdcard/ui.xml")).iter("node"))
    except ET.ParseError:
        return []  # a half-written dump while the screen changes; the caller tries again


def center(n):
    x1, y1, x2, y2 = map(int, re.findall(r"\d+", n.get("bounds")))
    return (x1 + x2) // 2, (y1 + y2) // 2


def tap(n):
    x, y = center(n)
    adb("shell", "input", "tap", str(x), str(y))


def wait(find, what, seconds=90):
    """Returns the first node find() gives; the app may still be starting."""
    for _ in range(seconds):
        ns = nodes()
        hit = find(ns)
        if hit is not None:
            return hit
        # A busy host can make Android ask "isn't responding": keep waiting.
        wait_btn = next((n for n in ns if n.get("text") == "Wait"), None)
        if wait_btn is not None:
            tap(wait_btn)
        time.sleep(1)
    sys.exit(f"no {what} on screen after {seconds} s")


if sys.argv[1] == "dump":
    print(" | ".join(n.get("text", "") for n in nodes() if n.get("text")))
elif sys.argv[1] == "wait":
    wait(lambda ns: next((n for n in ns if sys.argv[2].lower() in n.get("text", "").lower()), None), f"text {sys.argv[2]!r}")
elif sys.argv[1] == "toggle":
    tap(wait(lambda ns: next((n for n in ns if n.get("checkable") == "true"), None), "switch"))
elif sys.argv[1] == "tap":
    tap(wait(lambda ns: next((n for n in ns if n.get("text", "").lower() == sys.argv[2].lower()), None), f"button {sys.argv[2]!r}"))
else:
    i = int(sys.argv[2])
    fields = lambda ns: [n for n in ns if n.get("class") == "android.widget.EditText"]
    tap(wait(lambda ns: fields(ns)[i] if len(fields(ns)) > i else None, f"field {i}"))
    adb("shell", "input", "keyevent", "KEYCODE_MOVE_END")
    adb("shell", "input", "keyevent", *["KEYCODE_DEL"] * 60)  # clear a saved value
    adb("shell", "input", "text", sys.argv[3].replace(" ", "%s"))
    adb("shell", "input", "keyevent", "KEYCODE_BACK")  # close the keyboard
