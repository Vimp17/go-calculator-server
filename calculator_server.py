#!/usr/bin/env python3

import argparse
import ctypes
import os
import signal
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlparse, parse_qs

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))

# ---------------------------------------------------------------------------
# Shared state
# ---------------------------------------------------------------------------
state_lock = threading.Lock()
sum_value = 0
sub_value = 0


def load_libraries(c_lib_path, rust_lib_path):
    c_lib = ctypes.CDLL(c_lib_path)
    c_lib.add.argtypes = [ctypes.c_int64, ctypes.c_int64]
    c_lib.add.restype = ctypes.c_int64

    rust_lib = ctypes.CDLL(rust_lib_path)
    rust_lib.sub.argtypes = [ctypes.c_int64, ctypes.c_int64]
    rust_lib.sub.restype = ctypes.c_int64

    return c_lib, rust_lib


class CalcHandler(BaseHTTPRequestHandler):
    c_lib = None
    rust_lib = None

    def do_POST(self):
        global sum_value, sub_value

        parsed = urlparse(self.path)
        if parsed.path != "/calc":
            self._respond(404, b"not found")
            return

        query = parse_qs(parsed.query)
        raw_num = query.get("num", [None])[0]
        if raw_num is None:
            self._respond(400, b"missing 'num' query parameter")
            return

        try:
            num = int(raw_num)
        except ValueError:
            self._respond(400, b"'num' must be an integer")
            return

        with state_lock:
            sum_value = self.c_lib.add(sum_value, num)
            sub_value = self.rust_lib.sub(sub_value, num)

        self._respond(200, b"ok")

    def _respond(self, code, body=b""):
        self.send_response(code)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        if body:
            self.wfile.write(body)

    def log_message(self, format, *args):
        pass


def print_totals(label):
    with state_lock:
        print(f"[{label}] sum={sum_value} sub={sub_value}", flush=True)


def periodic_printer(stop_event, interval=5.0):
    while not stop_event.wait(interval):
        print_totals("periodic")


def main():
    parser = argparse.ArgumentParser(description="Calculator HTTP server")
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=8080)
    parser.add_argument(
        "--c-lib",
        default=os.path.join(SCRIPT_DIR, "libcalculator.so"),
        help="path to the compiled C shared library",
    )
    parser.add_argument(
        "--rust-lib",
        default=os.path.join(SCRIPT_DIR, "libcalculator_rust.so"),
        help="path to the compiled Rust shared library",
    )
    parser.add_argument(
        "--interval",
        type=float,
        default=5.0,
        help="seconds between periodic sum/sub reports",
    )
    args = parser.parse_args()

    try:
        c_lib, rust_lib = load_libraries(args.c_lib, args.rust_lib)
    except OSError as exc:
        print(f"Failed to load native libraries: {exc}", file=sys.stderr)
        print("Did you run build.sh first?", file=sys.stderr)
        sys.exit(1)

    CalcHandler.c_lib = c_lib
    CalcHandler.rust_lib = rust_lib

    server = ThreadingHTTPServer((args.host, args.port), CalcHandler)

    stop_event = threading.Event()
    printer_thread = threading.Thread(
        target=periodic_printer, args=(stop_event, args.interval), daemon=True
    )
    printer_thread.start()

    def handle_sigint(signum, frame):
        print("\nSIGINT received, shutting down...", flush=True)
        print_totals("final")
        stop_event.set()
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGINT, handle_sigint)

    print(f"Calculator server listening on {args.host}:{args.port}", flush=True)
    server.serve_forever()
    server.server_close()


if __name__ == "__main__":
    main()
