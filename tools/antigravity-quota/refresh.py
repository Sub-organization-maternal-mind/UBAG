#!/usr/bin/env python3
import argparse
import json
import os
import subprocess
import sys
import urllib.request
import urllib.error


def main():
    parser = argparse.ArgumentParser(description="Refresh Antigravity quota data")
    parser.add_argument("--gateway", default=os.environ.get("UBAG_GATEWAY_URL", "http://localhost:8080"))
    parser.add_argument("--secret", default=os.environ.get("UBAG_APP_SECRET", "dev-secret"))
    parser.add_argument("--agy-quota-bin", default=os.environ.get("AGY_QUOTA_BIN", "agy-quota"))
    args = parser.parse_args()

    try:
        result = subprocess.run(
            [args.agy_quota_bin, "-c", "-d"],
            capture_output=True,
            text=True,
            timeout=30,
        )
        if result.returncode != 0:
            print(f"agy-quota failed: {result.stderr}", file=sys.stderr)
            sys.exit(1)

        quota_data = json.loads(result.stdout)

        req = urllib.request.Request(
            f"{args.gateway}/v1/quotas/antigravity/refresh",
            data=json.dumps(quota_data).encode("utf-8"),
            headers={
                "Content-Type": "application/json",
                "Authorization": f"Bearer {args.secret}",
            },
            method="POST",
        )

        with urllib.request.urlopen(req, timeout=15) as resp:
            if resp.status != 200:
                print(f"Gateway returned {resp.status}: {resp.read().decode()}", file=sys.stderr)
                sys.exit(1)

        print("Quota data refreshed successfully")
    except Exception as exc:
        print(f"Failed to refresh quota: {exc}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
