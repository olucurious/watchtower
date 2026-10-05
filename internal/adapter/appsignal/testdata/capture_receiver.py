# Bounded capture receiver: saves request bodies and non-credential headers.
import http.server, json, os, sys, itertools
OUT=sys.argv[2]; os.makedirs(OUT, exist_ok=True); n=itertools.count(1)
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        if self.headers.get('transfer-encoding','').lower()=='chunked':
            body=b''
            while True:
                size=int(self.rfile.readline().split(b';')[0].strip(),16)
                if size==0: self.rfile.readline(); break
                body+=self.rfile.read(size); self.rfile.readline()
        else: body=self.rfile.read(int(self.headers.get('content-length',0)))
        i=next(n)
        meta={'method':'POST','path':self.path.split('?')[0],
              'query_keys':sorted(p.split('=')[0] for p in self.path.partition('?')[2].split('&') if p),
              'headers':{k.lower():v for k,v in self.headers.items() if k.lower() in('content-type','content-encoding','user-agent','transfer-encoding')},
              'auth_header_present':'x-sentry-auth' in {k.lower() for k in self.headers}}
        open(f'{OUT}/req-{i}.json','w').write(json.dumps(meta,indent=2)); open(f'{OUT}/req-{i}.bin','wb').write(body)
        self.send_response(200); self.send_header('content-type','application/json'); self.end_headers(); self.wfile.write(b'{"id":"x"}')
    def log_message(self,*a): pass
http.server.HTTPServer(('127.0.0.1',int(sys.argv[1])),H).serve_forever()
