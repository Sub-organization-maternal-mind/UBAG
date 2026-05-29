FROM python:3.12-slim

WORKDIR /app

COPY apps/worker ./apps/worker
COPY adapters ./adapters

CMD ["python", "apps/worker/run_mock_worker.py", "--help"]
