#!/usr/bin/env python3

import argparse
import random
import threading
import time
import urllib.error
import urllib.request
from urllib.parse import urlencode


def worker(worker_id, base_url, stop_event, interval, timeout, stats):
    while not stop_event.is_set():
        num = random.randint(-100, 100)
        url = f"{base_url}?{urlencode({'num': num})}"
        req = urllib.request.Request(url, method="POST", data=b"")
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                resp.read()
                with stats["lock"]:
                    stats["ok"] += 1
        except (urllib.error.URLError, OSError) as exc:
            with stats["lock"]:
                stats["errors"] += 1
            print(f"[worker {worker_id}] request failed: {exc}", flush=True)

        if interval > 0:
            stop_event.wait(interval)


def main():
    parser = argparse.ArgumentParser(description="Load generator for the calculator")
    parser.add_argument(
        "--url", default="http://localhost:8080/calc", help="calculator endpoint"
    )
    parser.add_argument(
        "-n", "--threads", type=int, default=10, help="number of worker threads"
    )
    parser.add_argument(
        "--interval",
        type=float,
        default=0.1,
        help="pause between requests per thread, in seconds (0 = as fast as possible)",
    )
    parser.add_argument(
        "--timeout", type=float, default=5.0, help="HTTP request timeout, seconds"
    )
    args = parser.parse_args()

    stop_event = threading.Event()
    stats = {"lock": threading.Lock(), "ok": 0, "errors": 0}

    threads = []
    for i in range(args.threads):
        t = threading.Thread(
            target=worker,
            args=(i, args.url, stop_event, args.interval, args.timeout, stats),
            daemon=True,
        )
        t.start()
        threads.append(t)

    print(f"Generator started: {args.threads} threads -> {args.url}", flush=True)

    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        print("\nSIGINT received, stopping generator...", flush=True)
        stop_event.set()
        for t in threads:
            t.join(timeout=2)
        with stats["lock"]:
            print(f"Total requests: ok={stats['ok']} errors={stats['errors']}")


if __name__ == "__main__":
    main()
