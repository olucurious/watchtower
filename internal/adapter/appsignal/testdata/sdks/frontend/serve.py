# Serves the page and records /collect posts.
import http.server, json, os, sys, itertools
os.makedirs("out", exist_ok=True); n = itertools.count(1)
class H(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *a, **k): super().__init__(*a, directory="static", **k)
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("content-length", 0))); i = next(n)
        q = self.path.partition("?")[2]
        json.dump({"method": "POST", "path": self.path.split("?")[0], "query_keys": sorted(p.split("=")[0] for p in q.split("&") if p),
                   "headers": {"content-type": self.headers.get("content-type")}}, open(f"out/req-{i}.json", "w"), indent=2)
        open(f"out/req-{i}.bin", "wb").write(body)
        self.send_response(200); self.send_header("content-type", "application/json"); self.end_headers(); self.wfile.write(b"{}")
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", 18905), H).serve_forever()
