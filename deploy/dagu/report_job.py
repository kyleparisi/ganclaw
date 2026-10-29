#!/usr/bin/env python3
"""Keep actual Dagu notifications visible and completion receipts in Jobs."""
import argparse
import http.client
import json
import os
from pathlib import Path
import signal
import socket
import socketserver
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler

UPSTREAM = os.environ.get('GANCLAW_SOCKET', '')

class Connection(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(UPSTREAM)

def forward(method, path, body=None):
    conn = Connection('localhost', timeout=3600)
    try:
        conn.request(method, path, body, {'Content-Type':'application/json'})
        response = conn.getresponse()
        return response.status, response.read()
    finally:
        conn.close()

def route(body, job, agent, is_run=False, account_agents=None):
    body = dict(body)
    if not is_run or body.get('to'):
        target = body.get('to', '')
        if target.startswith('telegram:'):
            account = target.split(':')[1]
            agent = (account_agents or {}).get(account, agent)
        elif target.startswith('app:'):
            agent = target.removeprefix('app:')
        body['to'] = 'app:' + agent
        body.pop('via', None)
        # Only the wrapper's final completion/failure receipt belongs in Jobs.
        # Actual reports must remain visible in the bot conversation. The
        # reserved background tag also prevents named agent sessions from
        # being inferred as scheduled-job-only delivery by the gateway.
        if is_run or body.get('failed'):
            body['job'] = 'background'
        else:
            body.pop('job', None)
        # Old Telegram idempotency receipts must not suppress app delivery.
        if body.get('idempotency_key'):
            body['idempotency_key'] = 'app-cron-visible:' + job + ':' + body['idempotency_key']
    return body

class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def handle_request(self):
        body = self.rfile.read(int(self.headers.get('Content-Length', 0))) or None
        try:
            if self.command == 'POST' and self.path in ('/v1/send','/v1/run'):
                data = route(json.loads(body), self.server.job, self.server.agent, self.path == '/v1/run', self.server.account_agents)
                body = json.dumps(data).encode()
            status, result = forward(self.command, self.path, body)
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(result)))
            self.end_headers()
            self.wfile.write(result)
        except Exception:
            self.send_error(502, 'Ganclaw app reporting request failed')
    do_GET = handle_request
    do_POST = handle_request
    do_DELETE = handle_request

def main():
    global UPSTREAM
    parser = argparse.ArgumentParser()
    parser.add_argument('--socket', default=UPSTREAM, help='Gateway Unix socket (default: GANCLAW_SOCKET)')
    parser.add_argument('--account-agent', action='append', default=[], metavar='ACCOUNT=AGENT',
                        help='Map a legacy Telegram account to an app agent; repeat as needed')
    parser.add_argument('--job', required=True)
    parser.add_argument('--agent', required=True)
    parser.add_argument('--step', required=True)
    parser.add_argument('--command-file', required=True)
    args = parser.parse_args()
    if not args.socket:
        parser.error('--socket or GANCLAW_SOCKET is required')
    UPSTREAM = args.socket
    account_agents = {}
    for mapping in args.account_agent:
        account, separator, agent = mapping.partition('=')
        if not separator or not account or not agent:
            parser.error('--account-agent must be ACCOUNT=AGENT')
        account_agents[account] = agent
    with tempfile.TemporaryDirectory(prefix='dagu-app-') as temp:
        proxy = str(Path(temp)/'api.sock')
        with Server(proxy, Handler) as server:
            os.chmod(proxy,0o600)
            server.job, server.agent = args.job, args.agent
            server.account_agents = account_agents
            threading.Thread(target=server.serve_forever, daemon=True).start()
            env = dict(os.environ, GANCLAW_SOCKET=proxy)
            process = subprocess.Popen(['/bin/sh','-c',Path(args.command_file).read_text()], env=env, start_new_session=True)
            def stop(signum, frame):
                os.killpg(process.pid, signum)
            signal.signal(signal.SIGTERM, stop)
            signal.signal(signal.SIGINT, stop)
            code = process.wait()
            # Do not copy arbitrary stdout, stderr, commands, or env into chat.
            body = {'to':'app:'+args.agent,'job':args.job,'failed':code != 0,
                    'text':args.job+' / '+args.step+(': completed.' if code == 0 else ': failed (exit '+str(code)+'). Check Dagu logs for details.')}
            status, _ = forward('POST','/v1/send',json.dumps(body).encode())
            server.shutdown()
            if status != 200:
                print('App job receipt delivery failed', flush=True)
                return code or 1
            return code if code >= 0 else 128-code

if __name__ == '__main__':
    raise SystemExit(main())
