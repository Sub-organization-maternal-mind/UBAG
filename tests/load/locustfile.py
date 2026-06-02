"""
UBAG load test — mixed read/write operator profile.
Usage: locust -f tests/load/locustfile.py --host http://localhost:8081
"""
import os
import json
import uuid
from locust import HttpUser, task, between

APP_SECRET = os.environ.get('UBAG_APP_SECRET', '')
API_VERSION = '2026-05-22'

BASE_HEADERS = {
    'Ubag-Api-Version': API_VERSION,
    'Authorization': f'Bearer {APP_SECRET}',
    'Content-Type': 'application/json',
}


class OperatorUser(HttpUser):
    wait_time = between(0.5, 2)

    @task(3)
    def check_health(self):
        with self.client.get('/v1/health', headers=BASE_HEADERS, catch_response=True) as res:
            if res.status_code == 200:
                res.success()
            else:
                res.failure(f'health failed: {res.status_code}')

    @task(5)
    def list_jobs(self):
        with self.client.get('/v1/jobs', headers=BASE_HEADERS, catch_response=True) as res:
            if res.status_code in (200, 403):
                res.success()
            else:
                res.failure(f'list jobs failed: {res.status_code}')

    @task(2)
    def create_job(self):
        payload = {
            'job': {
                'target': 'https://example.com',
                'command_type': 'fetch',
                'input': {'url': 'https://example.com'},
            },
            'client': {
                'app_id': 'locust',
                'app_version': '1.0.0',
                'sdk': {'name': 'locust', 'version': '1.0.0'},
            },
        }
        headers = {**BASE_HEADERS, 'Idempotency-Key': str(uuid.uuid4())}
        with self.client.post('/v1/jobs', data=json.dumps(payload), headers=headers, catch_response=True) as res:
            if res.status_code in (200, 201, 202):
                res.success()
            elif res.status_code == 403:
                res.success()  # expected if token lacks write access
            else:
                res.failure(f'create job failed: {res.status_code}')

    @task(1)
    def list_targets(self):
        with self.client.get('/v1/targets', headers=BASE_HEADERS, catch_response=True) as res:
            if res.status_code in (200, 403):
                res.success()
            else:
                res.failure(f'list targets failed: {res.status_code}')
