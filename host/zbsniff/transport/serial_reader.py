"""Read framed messages from one or more sniffer dongles and merge the streams.

Each serial port runs in its own thread (so multiple USB dongles / a hub carrier
work transparently), decoding frames and pushing them onto a shared queue. The
caller consumes parsed messages via iteration.
"""

from __future__ import annotations

import queue
import threading
import time
from typing import Iterator

import serial  # pyserial

from .. import proto
from .types import Tagged

__all__ = ["SerialReader", "Tagged"]


class SerialReader:
    def __init__(self, ports: list[str], baud: int = 921600, maxsize: int = 10000):
        self.ports = ports
        self.baud = baud
        self._q: "queue.Queue[Tagged]" = queue.Queue(maxsize=maxsize)
        self._threads: list[threading.Thread] = []
        self._stop = threading.Event()
        self._serials: dict[str, serial.Serial] = {}

    def start(self) -> None:
        for port in self.ports:
            # USB CDC ignores baud, but a real value keeps pyserial happy.
            ser = serial.Serial(port, self.baud, timeout=0.1)
            self._serials[port] = ser
            t = threading.Thread(target=self._reader, args=(port, ser), daemon=True)
            t.start()
            self._threads.append(t)

    def _reader(self, port: str, ser: serial.Serial) -> None:
        dec = proto.StreamDecoder()
        while not self._stop.is_set():
            try:
                data = ser.read(4096)
            except serial.SerialException:
                break
            if not data:
                continue
            now = time.time()
            for msg_type, payload in dec.feed(data):
                parsed = proto.parse_payload(msg_type, payload)
                if parsed is None:
                    continue
                tagged = Tagged(port, now, msg_type, parsed)
                try:
                    self._q.put_nowait(tagged)
                except queue.Full:
                    # Host-side backpressure: drop oldest to stay live.
                    try:
                        self._q.get_nowait()
                        self._q.put_nowait(tagged)
                    except queue.Empty:
                        pass

    def send(self, data: bytes, port: str | None = None) -> None:
        """Send a command frame to one port (or all if port is None)."""
        targets = [port] if port else list(self._serials)
        for p in targets:
            ser = self._serials.get(p)
            if ser:
                ser.write(data)

    def __iter__(self) -> Iterator[Tagged]:
        while not self._stop.is_set():
            try:
                yield self._q.get(timeout=0.25)
            except queue.Empty:
                continue

    def stop(self) -> None:
        self._stop.set()
        for ser in self._serials.values():
            try:
                ser.close()
            except Exception:
                pass
