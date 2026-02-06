# Deej Python Controller

Alternative implementation of the Deej controller logic in Python, optimized for **PulseAudio/PipeWire**.
This version uses native event listeners to detect new audio streams **immediately** and applies the volume processing, avoiding the "100% burst" issue.


## System Requirements

- **Desktop Environment:** Irrelevant (Works on KDE, GNOME, XFCE, i3, etc.)
- **Audio System:** **PulseAudio** OR **PipeWire** (with `pipewire-pulse` compatibility, which is standard on most modern distros like CachyOS, Manjaro, Ubuntu).
- **Permissions:** Your user needs access to the serial port.
  - Arch/CachyOS: Add user to `uucp` check via `groups`
  - Ubuntu/Debian: Add user to `dialout`
  ```bash
  sudo usermod -aG uucp $USER  # Arch/Manjaro
  # OR
  sudo usermod -aG dialout $USER # Ubuntu/Debian
  ```
  *(Logout/Login required after changing groups)*

## Setup

1. **Install Python** (usually already installed):
   ```bash
   sudo pacman -S python python-pip
   ```

2. **Install Dependencies:**
   ```bash
   cd deej-python-linux
   pip install -r requirements.txt
   ```
   *(Note: On some systems you might need to use a venv or `--break-system-packages`, or install packages via pacman: `python-pyserial-asyncio`, `python-pulsectl`, `python-setproctitle`)*

3. **Configuration:**
   The script currently has the configuration embedded in the header of `deej.py`. Please adjust `SLIDER_MAPPING` there if needed.


## Arduino Compatibility

This script works with the standard deej Arduino sketches, but we recommend the optimized version included in this repository:
`arduino/deej-4-sliders+keys/deej-4-sliders+keys_10.ino`
`arduino/deej-4-sliders+keys/deej-4-sliders+keys_12.ino`

**Adding more sliders:**
If you need a 5th slider (or more), simply edit the Arduino sketch to read an additional analog pin and add it to the `SLIDER_MAPPING` in `deej.py`.
The script automatically detects the number of sliders sent by the Arduino.
## Usage

```bash
chmod +x deej.py
./deej.py
```

## Setup as Service (Autostart)

1. Copy the service file:
   ```bash
   mkdir -p ~/.config/systemd/user/
   cp deej.service ~/.config/systemd/user/
   ```
   *(Adjust the path in the service file if necessary!)*

2. Enable and start:
   ```bash
   systemctl --user enable --now deej.service
   ```

## Troubleshooting / Tips

### Browser Volume Bursts (100% Volume on Start)
Even with this script, some browsers (Firefox, Chrome) might still briefly flash 100% volume because they initialize their audio stream aggressively.
**Workaround:** Configure the browser to use **ALSA** as its audio backend. This buffers the creation enough for Deej to catch it in time.
- **Firefox:** `about:config` -> search for `media.cubeb.backend` -> set to `alsa` (if available) or launch with `MOZ_AUDIO_BACKEND=alsa`.
