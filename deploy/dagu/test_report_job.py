import json
from pathlib import Path
import shlex
import socketserver
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler

from report_job import route


class ReportTests(unittest.TestCase):
    def test_actual_notification_remains_visible_and_does_not_mutate_input(self):
        original = {'to': 'telegram:alerts:123', 'via': 'alerts', 'job': 'health',
                    'text': 'Healthy', 'idempotency_key': 'daily'}
        result = route(original, 'health', 'support', account_agents={'alerts': 'alerts'})
        self.assertEqual('app:alerts', result['to'])
        self.assertNotIn('job', result)
        self.assertNotIn('via', result)
        self.assertEqual('app-cron-visible:health:daily', result['idempotency_key'])
        self.assertEqual('health', original['job'])
        self.assertEqual('telegram:alerts:123', original['to'])

    def test_agent_results_stay_visible_despite_a_named_session(self):
        body = {'to': 'telegram:sales:123', 'agent': 'sales', 'session': 'review',
                'quiet': True, 'async': True, 'prompt': 'Review'}
        result = route(body, 'inbox', 'sales', True)
        self.assertEqual('app:sales', result['to'])
        self.assertEqual('review', result['session'])
        self.assertTrue(result['quiet'])
        self.assertTrue(result['async'])
        self.assertEqual('background', result['job'])

    def test_return_value_only_recommendation_is_not_sent(self):
        body = {'agent': 'support', 'prompt': 'Classify this'}
        self.assertEqual(body, route(body, 'leads', 'support', True))

    def test_explicit_app_destination_is_preserved(self):
        result = route({'to': 'app:sales', 'text': 'Report'}, 'daily', 'support')
        self.assertEqual('app:sales', result['to'])

    def test_failed_report_remains_visible_with_error_status(self):
        result = route({'to': 'app:sales', 'job': 'old', 'failed': True,
                        'text': 'Check failed'}, 'health', 'sales')
        self.assertEqual('background', result['job'])
        self.assertTrue(result['failed'])


class WrapperTests(unittest.TestCase):
    def run_step(self, command):
        received = []

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                received.append((self.path, json.loads(self.rfile.read(
                    int(self.headers['Content-Length'])))))
                payload = b'{"ok":true}'
                self.send_response(200)
                self.send_header('Content-Length', str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

        with tempfile.TemporaryDirectory() as temp:
            upstream = str(Path(temp) / 'gateway.sock')
            with socketserver.UnixStreamServer(upstream, Handler) as server:
                thread = threading.Thread(target=server.serve_forever, daemon=True)
                thread.start()
                command_file = Path(temp) / 'step.sh'
                command_file.write_text(command)
                try:
                    result = subprocess.run([
                        sys.executable, str(Path(__file__).with_name('report_job.py')),
                        '--socket', upstream, '--job', 'daily', '--agent', 'support',
                        '--step', 'check', '--command-file', str(command_file),
                    ], capture_output=True, text=True, timeout=10)
                finally:
                    server.shutdown()
                    thread.join()
        return result, received

    def test_actual_report_and_completion_receipt_have_different_visibility(self):
        # The child uses the same Unix-socket API as a real scheduler script.
        code = '''import http.client, json, os, socket
c = http.client.HTTPConnection('localhost')
c.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
c.sock.connect(os.environ['GANCLAW_SOCKET'])
c.request('POST', '/v1/send', json.dumps({'to':'telegram:support:123', 'text':'New support request'}))
r = c.getresponse()
assert r.status == 200
r.read()
c.close()
print('Private stdout stays in Dagu logs')
'''
        result, received = self.run_step(shlex.join([sys.executable, '-c', code]))
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertEqual(2, len(received))
        self.assertEqual('/v1/send', received[0][0])
        notification = received[0][1]
        self.assertEqual('app:support', notification['to'])
        self.assertEqual('New support request', notification['text'])
        self.assertNotIn('job', notification)
        receipt = received[1][1]
        self.assertEqual('daily', receipt['job'])
        self.assertFalse(receipt['failed'])
        self.assertEqual('daily / check: completed.', receipt['text'])
        self.assertNotIn('Private stdout', receipt['text'])

    def test_failed_step_keeps_its_exit_code_and_failed_job_receipt(self):
        result, received = self.run_step('exit 7')
        self.assertEqual(7, result.returncode)
        self.assertEqual(1, len(received))
        self.assertEqual('daily', received[0][1]['job'])
        self.assertTrue(received[0][1]['failed'])


if __name__ == '__main__':
    unittest.main()
