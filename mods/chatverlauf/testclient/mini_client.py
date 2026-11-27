"""Minimaler Minecraft-Testclient (offline-mode) für den Chatverlauf-Test.

Nur für einen LOKALEN Testserver mit online-mode=false, enforce-secure-profile=false und
network-compression-threshold=-1. Kein Rendering, keine Signaturen: Der Client tritt bei,
beantwortet Keep-Alive/Ping, kann unsignierte Chatnachrichten senden und schreibt alle
empfangenen Chat-Pakete (system_chat, disguised_chat, player_chat) als JSON-Zeilen aus.

Paket-IDs kommen aus dem Bericht des Servers (packets.json), damit das Skript bei
Minecraft-Updates nicht angepasst werden muss:
    java -DbundlerMainClass=net.minecraft.data.Main -jar server.jar --reports --output gen

Aufruf:
    python -I mini_client.py --packets gen/reports/packets.json --protocol 776 \
        --port 25599 --name Tester1 [--say "Hallo" ...] [--wait 3]
"""

import argparse
import io
import json
import queue
import socket
import struct
import sys
import threading
import time
import uuid


# --- Grundtypen -----------------------------------------------------------------------------

def varint(n):
    n &= 0xFFFFFFFF
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        if n:
            out.append(b | 0x80)
        else:
            out.append(b)
            return bytes(out)


def read_varint(r):
    n = 0
    for i in range(5):
        b = r.read(1)
        if not b:
            raise EOFError
        n |= (b[0] & 0x7F) << (7 * i)
        if not b[0] & 0x80:
            break
    if n & 0x80000000:
        n -= 1 << 32
    return n


def string(s):
    b = s.encode("utf-8")
    return varint(len(b)) + b


def read_string(r):
    n = read_varint(r)
    return r.read(n).decode("utf-8")


# --- Netzwerk-NBT (Wurzel ohne Namen) -------------------------------------------------------

def read_nbt(r, tag=None):
    if tag is None:
        tag = r.read(1)[0]
    if tag == 0:
        return None
    if tag == 1:
        return struct.unpack(">b", r.read(1))[0]
    if tag == 2:
        return struct.unpack(">h", r.read(2))[0]
    if tag == 3:
        return struct.unpack(">i", r.read(4))[0]
    if tag == 4:
        return struct.unpack(">q", r.read(8))[0]
    if tag == 5:
        return struct.unpack(">f", r.read(4))[0]
    if tag == 6:
        return struct.unpack(">d", r.read(8))[0]
    if tag == 7:
        n = struct.unpack(">i", r.read(4))[0]
        return list(r.read(n))
    if tag == 8:
        n = struct.unpack(">H", r.read(2))[0]
        return r.read(n).decode("utf-8", "replace")
    if tag == 9:
        t = r.read(1)[0]
        n = struct.unpack(">i", r.read(4))[0]
        return [read_nbt(r, t) for _ in range(n)]
    if tag == 10:
        d = {}
        while True:
            t = r.read(1)[0]
            if t == 0:
                return d
            n = struct.unpack(">H", r.read(2))[0]
            name = r.read(n).decode("utf-8", "replace")
            d[name] = read_nbt(r, t)
    if tag == 11:
        n = struct.unpack(">i", r.read(4))[0]
        return list(struct.unpack(">%di" % n, r.read(4 * n)))
    if tag == 12:
        n = struct.unpack(">i", r.read(4))[0]
        return list(struct.unpack(">%dq" % n, r.read(8 * n)))
    raise ValueError("unbekannter NBT-Typ %d" % tag)


def plain(c):
    """Text einer Komponente ohne Stil (translatable: Schlüssel + Argumente)."""
    if c is None:
        return ""
    if isinstance(c, str):
        return c
    if isinstance(c, list):
        return "".join(plain(x) for x in c)
    s = c.get("text", "")
    if "translate" in c:
        s += "{%s:%s}" % (c["translate"], ",".join(plain(a) for a in c.get("with", [])))
    for x in c.get("extra", []):
        s += plain(x)
    return s


# --- Verbindung -----------------------------------------------------------------------------

class Conn:
    def __init__(self, host, port):
        self.s = socket.create_connection((host, port), timeout=10)
        self.f = self.s.makefile("rb")

    def send(self, pid, payload=b""):
        data = varint(pid) + payload
        self.s.sendall(varint(len(data)) + data)

    def recv(self):
        n = read_varint(self.f)
        data = self.f.read(n)
        r = io.BytesIO(data)
        pid = read_varint(r)
        return pid, r


def ids(packets, phase, direction):
    return {k.split(":", 1)[1]: v["protocol_id"] for k, v in packets[phase][direction].items()}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--packets", required=True)
    ap.add_argument("--protocol", type=int, required=True)
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=25599)
    ap.add_argument("--name", required=True)
    ap.add_argument("--say", action="append", default=[])
    ap.add_argument("--cmd", action="append", default=[])
    ap.add_argument("--spam", type=int, default=0)
    ap.add_argument("--delay", type=float, default=1.0, help="Sekunden bis zum Senden")
    ap.add_argument("--wait", type=float, default=3.0, help="Sekunden im Spiel nach dem Beitritt")
    a = ap.parse_args()
    if a.host not in ("127.0.0.1", "localhost", "::1"):
        sys.exit("nur für lokale Testserver")

    pk = json.load(open(a.packets, encoding="utf-8"))
    l_cb = ids(pk, "login", "clientbound")
    l_sb = ids(pk, "login", "serverbound")
    c_cb = ids(pk, "configuration", "clientbound")
    c_sb = ids(pk, "configuration", "serverbound")
    p_cb = ids(pk, "play", "clientbound")
    p_sb = ids(pk, "play", "serverbound")
    p_cb_name = {v: k for k, v in p_cb.items()}

    def out(**kw):
        print(json.dumps(kw, ensure_ascii=False), flush=True)

    c = Conn(a.host, a.port)
    c.send(0, varint(a.protocol) + string(a.host) + struct.pack(">H", a.port) + varint(2))
    off_uuid = uuid.uuid3(uuid.NAMESPACE_DNS, "x")  # Server rechnet die Offline-UUID selbst
    c.send(l_sb["hello"], string(a.name) + off_uuid.bytes)

    # Login
    while True:
        pid, r = c.recv()
        if pid == l_cb["login_finished"]:
            c.send(l_sb["login_acknowledged"])
            break
        if pid == l_cb["login_disconnect"]:
            out(ereignis="login_abgewiesen", grund=read_string(r))
            return 2
        if pid == l_cb["login_compression"]:
            sys.exit("Kompression an: network-compression-threshold=-1 setzen")
        if pid == l_cb["custom_query"]:
            c.send(l_sb["custom_query_answer"], varint(read_varint(r)) + b"\x00")
        if pid == l_cb["cookie_request"]:
            c.send(l_sb["cookie_response"], string(read_string(r)) + b"\x00")

    # Konfiguration
    chat_types = []
    while True:
        pid, r = c.recv()
        if pid == c_cb["select_known_packs"]:
            c.send(c_sb["select_known_packs"], r.read())  # gleiche Packs zurück
        elif pid == c_cb["registry_data"]:
            reg = read_string(r)
            if reg == "minecraft:chat_type":
                for _ in range(read_varint(r)):
                    chat_types.append(read_string(r))
                    if r.read(1) == b"\x01":
                        read_nbt(r)
        elif pid == c_cb["keep_alive"]:
            c.send(c_sb["keep_alive"], r.read())
        elif pid == c_cb["ping"]:
            c.send(c_sb["pong"], r.read())
        elif pid == c_cb.get("code_of_conduct"):
            c.send(c_sb["accept_code_of_conduct"])
        elif pid == c_cb["cookie_request"]:
            c.send(c_sb["cookie_response"], string(read_string(r)) + b"\x00")
        elif pid == c_cb["disconnect"]:
            out(ereignis="konfig_abgewiesen", grund=plain(read_nbt(r)))
            return 2
        elif pid == c_cb["finish_configuration"]:
            c.send(c_sb["finish_configuration"])
            break
    out(ereignis="beigetreten", name=a.name, chat_types=chat_types)

    # Spiel: ein Lesefaden, damit Wartezeiten nie mitten in einem Paket abbrechen.
    q = queue.Queue()

    def reader():
        try:
            while True:
                q.put(c.recv())
        except (EOFError, OSError):
            q.put(None)

    c.s.settimeout(None)
    threading.Thread(target=reader, daemon=True).start()
    start = time.time()
    said = False
    while time.time() - start < a.wait:
        if not said and time.time() - start > a.delay:
            for i, msg in enumerate(a.say):
                ts = int(time.time() * 1000)
                payload = (string(msg) + struct.pack(">q", ts) + struct.pack(">q", i + 1)
                           + b"\x00"            # keine Signatur
                           + varint(0) + b"\x00\x00\x00" + b"\x00")  # last seen: nichts, Prüfsumme aus
                c.send(p_sb["chat"], payload)
                out(ereignis="gesendet", text=msg)
                time.sleep(0.2)
            for i in range(a.spam):  # Massentest der Größengrenze (Spieler muss Op sein, sonst Spam-Kick)
                ts = int(time.time() * 1000)
                text = "Massentest %05d %s" % (i, "x" * 100)
                c.send(p_sb["chat"], string(text) + struct.pack(">q", ts) + struct.pack(">q", 1000 + i)
                       + b"\x00" + varint(0) + b"\x00\x00\x00" + b"\x00")
                time.sleep(0.01)
            if a.spam:
                out(ereignis="massentest_gesendet", anzahl=a.spam)
            for cmd in a.cmd:  # unsignierter Befehl ohne „/“, z. B. "me winkt"
                c.send(p_sb["chat_command"], string(cmd))
                out(ereignis="befehl", text=cmd)
                time.sleep(0.2)
            said = True
        try:
            item = q.get(timeout=0.2)
        except queue.Empty:
            continue
        if item is None:
            out(ereignis="verbindung_zu")
            return 3
        pid, r = item
        name = p_cb_name.get(pid)
        if name == "keep_alive":
            c.send(p_sb["keep_alive"], r.read())
        elif name == "ping":
            c.send(p_sb["pong"], r.read())
        elif name == "login":
            c.send(p_sb["player_loaded"])
        elif name == "system_chat":
            comp = read_nbt(r)
            overlay = r.read(1) == b"\x01"
            if not overlay:
                out(ereignis="paket", paket="system_chat", text=plain(comp), roh=comp)
        elif name == "disguised_chat":
            msg = read_nbt(r)
            holder = read_varint(r)
            typ = chat_types[holder - 1] if 0 < holder <= len(chat_types) else "direkt/%d" % holder
            sender = read_nbt(r)
            target = read_nbt(r) if r.read(1) == b"\x01" else None
            out(ereignis="paket", paket="disguised_chat", chat_type=typ, name=plain(sender),
                text=plain(msg), name_roh=sender, text_roh=msg, ziel=target)
        elif name == "player_chat":
            out(ereignis="paket", paket="player_chat")
        elif name == "disconnect":
            out(ereignis="getrennt", grund=plain(read_nbt(r)))
            return 3
    c.s.close()
    out(ereignis="ende")
    return 0


if __name__ == "__main__":
    sys.exit(main())
