# Deej Python Controller

Alternative implementation of the Deej controller logic in Python, optimized for **PulseAudio/PipeWire**.
This version uses native event listeners to detect new audio streams **immediately** and applies the volume processing, avoiding the "100% burst" issue.

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
