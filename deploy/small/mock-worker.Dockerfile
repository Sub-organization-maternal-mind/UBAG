# trivy:ignore:AVD-DS-0002 — mock test worker: runs only inside CI/test spool directories that are root-owned (also accepted in .trivyignore; the inline ID must carry the AVD- prefix to match)
FROM python:3.12-slim

WORKDIR /app

COPY apps/worker ./apps/worker
COPY adapters ./adapters

CMD ["python", "apps/worker/run_mock_worker.py", "--help"]
