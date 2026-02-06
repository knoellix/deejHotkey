# Deej Python Controller

Alternative Implementierung des Deej Controllers in Python, spezialisiert auf **PulseAudio/PipeWire**.
Diese Version nutzt native Event-Listener, um neue Audio-Streams **sofort** zu erkennen und die Lautstärke anzupassen, ohne den "100% Burst"-Effekt.

## Setup

1. **Python installieren** (meist schon vorhanden):
   ```bash
   sudo pacman -S python python-pip
   ```

2. **Dependencies installieren:**
   ```bash
   cd python-controller
   pip install -r requirements.txt
   ```
   *(Hinweis: Auf manchen Systemen muss man eine venv nutzen oder `--break-system-packages` verwenden, oder Pakete via pacman installieren: `python-pyserial-asyncio`, `python-pulsectl`, `python-setproctitle`)*

3. **Config anpassen:**
   Das Script hat die Konfiguration direkt im Header von `deej.py`. Bitte dort die Slider-Mappings anpassen bei Bedarf.

## Starten

```bash
chmod +x deej.py
./deej.py
```

## Als Service einrichten (Autostart)

1. Kopiere das Service-File:
   ```bash
   mkdir -p ~/.config/systemd/user/
   cp deej.service ~/.config/systemd/user/
   ```
   *(Passe den Pfad im Service-File an!)*

2. Aktivieren:
   ```bash
   systemctl --user enable --now deej.service
   ```
