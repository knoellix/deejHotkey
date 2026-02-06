#!/usr/bin/env python
import gc
import asyncio
import serial_asyncio
import pulsectl
import setproctitle
import sys
import subprocess
import logging
from pulsectl import PulseVolumeInfo
import os
import threading

# Internal constant, do not change
# This value represents "All other apps" that are not explicitly listed.
ALL_OTHER_APPS = "__all_other_apps__"

# --- CONFIGURATION START ---

# Logging level: "DEBUG", "INFO", "MINIMAL", "OFF"
# DEBUG: Everything (Debug + Info + Warnings + Errors)
# INFO: Normal operation (Info + Warnings + Errors)
# MINIMAL: Only problems (Warnings + Errors)
# OFF: No logs at all
LOG_LEVEL = "INFO"

# Log file path setup
LOG_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), 'deej.log')

# The USB port of your Arduino.
# If it changes (e.g. to /dev/ttyACM1), adjust it here.
SERIAL_PORT = '/dev/ttyACM0'

# Define which slider controls which programs here.
# Slider 0 is the first slider on the left, Slider 1 is next to it, etc.
SLIDER_MAPPING = {
    0: ["firefox", "google-chrome", "chromium", "brave", "web-browser"],
    1: ["spotify", "strawberry"],
    2: ["teamspeak", "ts3", "discord"],
    3: [ALL_OTHER_APPS]  # Slider 3 controls everything not listed above
}

# --- CONFIGURATION END ---



# Logging Setup
handlers = [
    logging.StreamHandler(),  # Console output
    logging.FileHandler(LOG_FILE)  # File output
]

# Map string level to logging constant
LEVEL_MAP = {
    "DEBUG": logging.DEBUG,
    "INFO": logging.INFO,
    "MINIMAL": logging.WARNING,  # Warnings + Errors
    "OFF": logging.CRITICAL + 10  # Higher than CRITICAL = essentially off
}

def cleanup_old_logs():
    """Remove log entries older than 6 hours from the log file."""
    from datetime import datetime, timedelta
    
    if not os.path.exists(LOG_FILE):
        return
    
    try:
        cutoff_time = datetime.now() - timedelta(hours=6)
        
        # Read all lines
        with open(LOG_FILE, 'r', encoding='utf-8') as f:
            lines = f.readlines()
        
        # Filter lines
        kept_lines = []
        for line in lines:
            if line.strip():
                try:
                    timestamp_str = line.split(' - ')[0]
                    log_time = datetime.strptime(timestamp_str, '%Y-%m-%d %H:%M:%S')
                    if log_time > cutoff_time:
                        kept_lines.append(line)
                except (ValueError, IndexError):
                    # If parsing fails, keep the line
                    kept_lines.append(line)
        
        # Write back (in-place)
        with open(LOG_FILE, 'w', encoding='utf-8') as f:
            f.writelines(kept_lines)
            
    except Exception:
        # If cleanup fails, just continue silently
        pass

# Clean old logs BEFORE logging is configured
cleanup_old_logs()

logging.basicConfig(
    level=LEVEL_MAP.get(LOG_LEVEL.upper(), logging.INFO),  # Default to INFO if invalid
    format='%(asctime)s - %(levelname)s - %(message)s',
    datefmt='%Y-%m-%d %H:%M:%S',
    handlers=handlers
)




class SerialReaderProtocol(asyncio.Protocol):
    def __init__(self, pulse_obj):
        self.pulse = pulse_obj
        self.buffer = b""
        self.last_volumes = None
        self.update_counter = 0
        self.connection_lost_future = asyncio.get_running_loop().create_future()

    def connection_made(self, transport):
        logging.info("Serial connection established!")

    def data_received(self, data):
        self.buffer += data
        while b'\n' in self.buffer:
            line, self.buffer = self.buffer.split(b'\n', 1)
            try:
                decoded_line = line.decode('utf-8').strip()
                self.process_line(decoded_line)
            except Exception as e:
                logging.error(f"Error decoding data: {e}")
                continue

    def process_line(self, line):
        parts = line.split('|')
        if len(parts) < len(SLIDER_MAPPING): 
             return

        try:
            slider_values = [int(val) for val in parts]
        except ValueError: return

        # Calculate volume for ALL received values
        current_volumes = [round(value / 1023 * 100) / 100 for value in slider_values]

        # CHECK: Have the sliders moved?
        if self.last_volumes != current_volumes:
            self.last_volumes = current_volumes
            # Sliders moved -> Apply volume explicitly
            self.apply_volumes(current_volumes)

    def apply_volumes(self, volumes=None):
        """
        Applies volume to all sinks. Can be called from Serial loop OR Event Thread.
        If volumes is None, uses last known volumes.
        """
        logging.debug(f"apply_volumes called, volumes={volumes}, last_volumes={self.last_volumes}")
        if volumes is None:
            volumes = self.last_volumes
        
        # If we don't have volume data yet, we can't do anything
        if volumes is None:
            logging.debug("Skipping volume update: No volume data from Arduino yet.")
            return

        try:
            # We must use the main pulse object here, but be careful about Thread Safety.
            # pulsectl is not thread-safe if sharing the same connection object across threads.
            # However, this method is called via loop.call_soon_threadsafe from the event thread,
            # so it actually runs on the MAIN thread (asyncio loop). This is safe!
            all_sinks = self.pulse.sink_input_list()
            
            # Find fallback volume
            fallback_volume = None
            for slider_idx, apps_list in SLIDER_MAPPING.items():
                if ALL_OTHER_APPS in apps_list:
                    if slider_idx < len(volumes):
                        fallback_volume = volumes[slider_idx]
                    break

            for sink in all_sinks:
                props = sink.proplist
                app_info = f"{props.get('application.name','')}{props.get('application.process.binary','')}{props.get('media.name','')}".lower()

                assigned_volume = None

                for slider_idx, apps_list in SLIDER_MAPPING.items():
                    if ALL_OTHER_APPS in apps_list: continue
                        
                    if any(assigned_app in app_info for assigned_app in apps_list):
                        if slider_idx < len(volumes):
                            assigned_volume = volumes[slider_idx]
                        break
                
                if assigned_volume is None:
                    assigned_volume = fallback_volume

                if assigned_volume is not None:
                     self.set_volume(sink, assigned_volume)

        except Exception as e:
            logging.error(f"Error applying volumes: {e}", exc_info=True)

        # Garbage Collection (kept simple)
        self.update_counter += 1
        if self.update_counter > 50:
            gc.collect()
            self.update_counter = 0

    def set_volume(self, sink, volume):
        try:
            if abs(sink.volume.value_flat - volume) >= 0.01:
                new_vol = PulseVolumeInfo([volume] * len(sink.volume.values))
                self.pulse.volume_set(sink, new_vol)
        except Exception as e:
             logging.warning(f"Could not set volume: {e}")

    def connection_lost(self, exc):
        logging.warning("Connection lost!")
        if not self.connection_lost_future.done():
            self.connection_lost_future.set_result(True)

def pulse_monitor_thread(loop, protocol):
    """
    Runs in a separate thread. Listens for PulseAudio events.
    Immediately applies correct volume when new apps start (system default is 20%).
    """
    with pulsectl.Pulse('deej-event-listener') as pulse_monitor:
        logging.info("Event Listener started.")

        def event_handler(event):
            if event.facility == 'sink_input' and event.t == 'new':
                # New app detected - apply correct volume immediately
                logging.debug("New app detected! Applying correct volume...")
                loop.call_soon_threadsafe(protocol.apply_volumes)

        pulse_monitor.event_mask_set('sink_input')
        pulse_monitor.event_callback_set(event_handler)
        pulse_monitor.event_listen()  # Blocking call

async def volume_watchdog(protocol):
    """
    Backup sync: Periodically applies stored slider levels to all apps.
    This ensures everything stays in sync if an app bypasses standard controls.
    """
    while True:
        await asyncio.sleep(0.5)  # Check every 500ms (fast sync)
        if protocol.last_volumes is not None:
            protocol.apply_volumes()

async def main():
    logging.info("Starting deej-service...")
    setproctitle.setproctitle("deej-service")

    try:
        pulse = pulsectl.Pulse('audio-control')
    except Exception as e:
        logging.critical(f"Could not connect to PulseAudio: {e}")
        sys.exit(1)

    loop = asyncio.get_running_loop()
    serial_port = SERIAL_PORT

    protocol_instance = SerialReaderProtocol(pulse)

    # Start Event Listener Thread for instant reactions
    t = threading.Thread(target=pulse_monitor_thread, args=(loop, protocol_instance), daemon=True)
    t.start()
    
    logging.info("Event listener started.")
    
    # Start slow periodic backup sync
    asyncio.create_task(volume_watchdog(protocol_instance))
    
    retry_count = 0
    notified = False

    while True:
        try:
            logging.info(f"Attempting connection to {serial_port}...")
            
            transport, protocol = await serial_asyncio.create_serial_connection(
                loop, lambda: protocol_instance, serial_port, baudrate=9600
            )
            
            if notified:
                subprocess.run(["/usr/bin/notify-send", "-i", "audio-card", "Deej Controller", "Connection restored!"])
                notified = False
                logging.info("Notification sent: Connection restored.")

            retry_count = 0
            await protocol_instance.connection_lost_future
            
            # Reset future for next connection attempt
            protocol_instance.connection_lost_future = loop.create_future()

        except Exception as e:
            logging.error(f"Connection error: {e}")
            retry_count += 1
            
            if retry_count == 6 and not notified:
                subprocess.run([
                    "/usr/bin/notify-send",
                    "-u", "critical",
                    "-i", "dialog-warning",
                    "Deej Controller",
                    "Controller not found! Please check USB connection."
                ])
                notified = True
                logging.warning("Critical notification sent: Controller not found.")

        await asyncio.sleep(5)


if __name__ == '__main__':
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        logging.info("Terminated by user.")
        sys.exit(0)
    except Exception as e:
        logging.critical(f"Unexpected crash: {e}", exc_info=True)
        sys.exit(1)
