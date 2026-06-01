"""Validate that a SQLite migration file executes cleanly against a temp database."""
import sqlite3
import os
import sys
import tempfile

if len(sys.argv) < 2:
    print("Usage: python validate-sqlite-migration.py <migration.sql>")
    sys.exit(1)

sql_path = sys.argv[1]
sql = open(sql_path, encoding="utf-8").read()

with tempfile.NamedTemporaryFile(suffix=".db", delete=False) as f:
    dbpath = f.name

try:
    conn = sqlite3.connect(dbpath)
    conn.execute(
        "CREATE TABLE IF NOT EXISTS edge_schema_migrations "
        "(version TEXT PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL)"
    )
    conn.commit()
    conn.executescript(sql)
    conn.commit()
    tables = conn.execute(
        "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"
    ).fetchall()
    print(f"PASS: {sql_path}")
    print("Tables created:", [t[0] for t in tables if not t[0].startswith("sqlite_")])
finally:
    conn.close()
    os.unlink(dbpath)
