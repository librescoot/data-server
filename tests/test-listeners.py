#!/usr/bin/env python3
"""Root-only isolated network-namespace acceptance tests; no host firewall changes."""
import argparse
import http.client
import json
import os
import pathlib
import socket
import subprocess
import sys
import tempfile
import time
import uuid

PORT = 18080
CANON4 = "192.0.2.99"
CANON6 = "2001:db8::99"


def request(spec):
    try:
        source = (spec["source"], 0) if spec.get("source") else None
        c = http.client.HTTPConnection(spec["host"], PORT, timeout=0.5, source_address=source)
        c.request(spec.get("method", "GET"), spec.get("path", "/"), spec.get("body"), spec.get("headers", {}))
        r = c.getresponse()
        result = {"status": r.status, "body": r.read().decode(), "server": r.getheader("Server")}
        c.close()
        return result
    except (OSError, http.client.HTTPException) as e:
        return {"error": type(e).__name__}


def run(*args):
    return subprocess.run(args, check=True, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10).stdout


def main(binary):
    if os.geteuid() != 0:
        raise SystemExit("Run with sudo; all network changes are confined to new namespaces")
    prefix = "ds-" + uuid.uuid4().hex[:8]
    namespaces = []
    server_ns = prefix + "-s"
    checks = 0
    process = None
    keeper = None

    def namespace(name):
        run("ip", "netns", "add", name)
        namespaces.append(name)
        run("ip", "-n", name, "link", "set", "lo", "up")
        return name

    def ip(*args):
        return run("ip", "-n", server_ns, *args)

    def peer_request(peer, host, **kwargs):
        spec = dict(host=host, **kwargs)
        output = run("ip", "netns", "exec", peer, sys.executable, __file__, "--request", json.dumps(spec))
        return json.loads(output)

    def allow(peer, host, status=200, **kwargs):
        nonlocal checks
        result = peer_request(peer, host, **kwargs)
        assert result.get("status") == status, (peer, host, kwargs, result)
        assert result["server"].startswith("librescoot-data-server/"), result
        checks += 1
        return result

    def ready(peer, host):
        end = time.monotonic() + 6
        while time.monotonic() < end:
            if process.poll() is not None:
                raise AssertionError("server exited before listener became available")
            if peer_request(peer, host).get("status") == 200:
                return
            time.sleep(0.1)
        raise AssertionError((peer, host, "listener did not appear"))

    def deny(peer, host, source=None):
        nonlocal checks
        # Require TCP denial, not merely an HTTP error or a concealed banner.
        spec = json.dumps(dict(host=host, source=source))
        result = run("ip", "netns", "exec", peer, sys.executable, __file__, "--connect", spec)
        assert result.strip() == "blocked", (peer, host, source, result)
        checks += 1

    def link(name, subnet4, subnet6, peer=None):
        peer = peer or namespace(prefix + "-" + name)
        ip("link", "add", name, "type", "veth", "peer", "name", "peer0", "netns", peer)
        ip("addr", "add", subnet4+".1/24", "dev", name)
        ip("-6", "addr", "add", subnet6+"::1/64", "dev", name, "nodad")
        ip("link", "set", name, "up")
        run("ip", "-n", peer, "addr", "add", subnet4+".2/24", "dev", "peer0")
        run("ip", "-n", peer, "-6", "addr", "add", subnet6+"::2/64", "dev", "peer0", "nodad")
        run("ip", "-n", peer, "link", "set", "peer0", "up")
        run("ip", "-n", peer, "route", "replace", "default", "via", subnet4+".1")
        run("ip", "-n", peer, "route", "replace", CANON4+"/32", "via", subnet4+".1")
        run("ip", "-n", peer, "-6", "route", "replace", "default", "via", subnet6+"::1")
        return peer

    with tempfile.TemporaryDirectory(prefix="data-server-listeners-") as temp:
        data = pathlib.Path(temp)/"data"
        data.mkdir()
        logfile = open(pathlib.Path(temp)/"server.log", "w+")
        try:
            namespace(server_ns)
            run("ip", "netns", "exec", server_ns, "sysctl", "-qw", "net.ipv4.conf.all.rp_filter=0", "net.ipv4.conf.default.rp_filter=0")
            ip("addr", "add", CANON4+"/32", "dev", "lo")
            ip("-6", "addr", "add", CANON6+"/128", "dev", "lo", "nodad")
            wan = link("wwan0", "203.0.113", "2001:db8:4")
            ethernet = link("end0", "198.18.0", "2001:db8:5")

            def start(address):
                nonlocal process
                process = subprocess.Popen(["ip", "netns", "exec", server_ns, binary, "-addr", address, "-data", str(data)], stdout=logfile, stderr=logfile)

            def stop():
                nonlocal process
                process.terminate()
                try: process.wait(timeout=4)
                except subprocess.TimeoutExpired: process.kill(); process.wait(); raise
                assert process.returncode == 0, process.returncode
                process = None

            start(":"+str(PORT))
            ready(server_ns, "127.0.0.1")
            allow(server_ns, "127.0.0.1")
            allow(server_ns, "::1")
            for host in (CANON4, CANON6, "203.0.113.1", "2001:db8:4::1"):
                deny(wan, host)
            deny(ethernet, CANON4)
            deny(ethernet, CANON6)

            # Trusted interfaces appear after startup; no service restart is needed.
            usb = link("usb0", "192.0.2", "2001:db8:1")
            ppp = link("ppp0", "198.51.100", "2001:db8:2")
            wg = link("wg0", "10.7.0", "2001:db8:3")
            for peer in (usb, ppp, wg):
                ready(peer, CANON4)
                allow(peer, CANON4)
                allow(peer, CANON6)
            allow(usb, "203.0.113.1")
            for host in ("192.0.2.1", "198.51.100.1", "10.7.0.1", "2001:db8:1::1", "2001:db8:3::1"):
                deny(wan, host)

            # A peer address resembling a VPN address does not confer ingress trust.
            run("ip", "-n", wan, "addr", "add", "10.7.0.99/32", "dev", "peer0")
            ip("route", "add", "10.7.0.99/32", "dev", "wwan0")
            deny(wan, CANON4, source="10.7.0.99")

            allow(usb, CANON4, method="PUT", path="/nested/test", body="abcdefgh")
            assert allow(ppp, CANON4, path="/nested/test")["body"] == "abcdefgh"
            allow(wg, CANON6, method="HEAD", path="/nested/test")
            assert allow(usb, CANON4, status=206, path="/nested/test", headers={"Range":"bytes=2-5"})["body"] == "cdef"
            for method in ("POST", "PATCH"):
                allow(usb, CANON4, method=method, path="/nested/test", body=method)
            assert "<html" in allow(usb, CANON4, headers={"Accept":"text/html"})["body"]
            allow(usb, CANON4, status=204, method="DELETE", path="/nested/test")
            assert not (data/"nested/test").exists()

            # Address changes do not require rebinding the device-bound wildcard socket.
            ip("addr", "del", "192.0.2.1/24", "dev", "usb0")
            ip("addr", "add", "192.0.2.9/24", "dev", "usb0")
            ip("-6", "addr", "del", "2001:db8:1::1/64", "dev", "usb0")
            ip("-6", "addr", "add", "2001:db8:1::9/64", "dev", "usb0", "nodad")
            allow(usb, "2001:db8:1::9")
            deny(wan, "2001:db8:1::9")
            allow(usb, "192.0.2.9")
            deny(wan, "192.0.2.9")
            ip("link", "set", "usb0", "down")
            deny(usb, "192.0.2.9")
            allow(ppp, CANON4)
            allow(wg, CANON6)
            ip("link", "set", "usb0", "up")
            ready(usb, "192.0.2.9")
            allow(usb, "192.0.2.9")

            # Renaming revokes listeners and already accepted keep-alive connections.
            keeper = subprocess.Popen(["ip", "netns", "exec", usb, sys.executable, __file__, "--keepalive", "192.0.2.9"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
            assert keeper.stdout.readline().strip() == "ready"
            ip("link", "set", "usb0", "down")
            ip("link", "set", "usb0", "name", "untrusted0")
            ip("link", "set", "untrusted0", "up")
            time.sleep(2)
            keeper.stdin.write("again\n"); keeper.stdin.flush()
            assert keeper.communicate(timeout=3)[0].strip() == "closed"
            checks += 1
            keeper = None
            deny(usb, "192.0.2.9")
            ip("link", "del", "untrusted0")
            deny(wan, CANON4)
            usb = link("usb0", "192.0.2", "2001:db8:1", peer=usb)
            ready(usb, CANON4)
            allow(usb, CANON4)
            allow(usb, CANON6)
            # WireGuard- and PPP-named links use the same recreation path.
            for name, peer, v4, v6 in (("ppp0", ppp, "198.51.100", "2001:db8:2"), ("wg0", wg, "10.7.0", "2001:db8:3")):
                ip("link", "del", name)
                deny(wan, CANON4)
                link(name, v4, v6, peer=peer)
                ready(peer, CANON4)
                allow(peer, CANON4)
                allow(peer, CANON6)
            stop()

            # Packaged board-address binds still enforce ingress, including addresses on lo.
            for address, host in ((CANON4+":"+str(PORT), CANON4), ("["+CANON6+"]:"+str(PORT), CANON6), ("203.0.113.1:"+str(PORT), "203.0.113.1"), ("[2001:db8:4::1]:"+str(PORT), "2001:db8:4::1")):
                start(address)
                for peer in (usb, ppp, wg):
                    ready(peer, host)
                    allow(peer, host)
                deny(wan, host)
                deny(ethernet, host)
                stop()

            # A specific service address can appear after the process has started.
            late = "192.0.2.100"
            start(late+":"+str(PORT))
            time.sleep(1)
            assert process.poll() is None
            deny(wan, CANON4)
            ip("addr", "add", late+"/32", "dev", "lo")
            ready(ppp, late)
            allow(ppp, late)
            deny(wan, late)
            stop()
            print(f"PASS: {checks} interface-bound listener and HTTP checks")
        except BaseException:
            logfile.flush(); logfile.seek(0)
            print(logfile.read(), file=sys.stderr)
            raise
        finally:
            if keeper is not None:
                keeper.kill(); keeper.wait()
            if process is not None:
                process.terminate()
                try: process.wait(timeout=3)
                except subprocess.TimeoutExpired: process.kill(); process.wait()
            for name in reversed(namespaces):
                run("ip", "netns", "delete", name)
            logfile.close()


if __name__ == "__main__":
    if len(sys.argv)>1 and sys.argv[1] == "--request":
        print(json.dumps(request(json.loads(sys.argv[2]))))
    elif len(sys.argv)>1 and sys.argv[1] == "--connect":
        spec=json.loads(sys.argv[2])
        try:
            c=socket.create_connection((spec["host"],PORT), timeout=0.35, source_address=(spec["source"],0) if spec.get("source") else None)
            c.close(); print("connected")
        except OSError: print("blocked")
    elif len(sys.argv)>1 and sys.argv[1] == "--keepalive":
        c=http.client.HTTPConnection(sys.argv[2],PORT,timeout=1)
        c.request("GET","/"); r=c.getresponse(); r.read()
        assert r.status==200
        print("ready",flush=True); sys.stdin.readline()
        try:
            c.request("GET","/"); c.getresponse().read(); print("open")
        except (OSError,http.client.HTTPException): print("closed")
        finally: c.close()
    else:
        parser=argparse.ArgumentParser(description=__doc__)
        parser.add_argument("--binary", required=True, help="host Linux data-server binary")
        args=parser.parse_args()
        main(str(pathlib.Path(args.binary).resolve()))
