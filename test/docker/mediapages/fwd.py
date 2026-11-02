"""Forwards a TCP port: fwd.py <listen port> <target port>.

Headless Chromium listens for DevTools on 127.0.0.1 alone; this makes it
reachable through the container's published port.
"""
import socket
import sys
import threading


def pipe(a, b):
    try:
        while True:
            data = a.recv(65536)
            if not data:
                break
            b.sendall(data)
    except OSError:
        pass
    finally:
        for s in (a, b):
            try:
                s.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass


def main():
    listen, target = int(sys.argv[1]), int(sys.argv[2])
    srv = socket.socket()
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("0.0.0.0", listen))
    srv.listen(64)
    while True:
        c, _ = srv.accept()
        try:
            u = socket.create_connection(("127.0.0.1", target), timeout=5)
            u.settimeout(None)
        except OSError:
            c.close()
            continue
        threading.Thread(target=pipe, args=(c, u), daemon=True).start()
        threading.Thread(target=pipe, args=(u, c), daemon=True).start()


main()
