"""Tests for worker region pinning — runs offline, no NATS needed."""
import importlib
import os
import pytest


def reload_region():
    from ubag_worker import region
    importlib.reload(region)
    return region


def test_subject_filter_default_region(monkeypatch):
    monkeypatch.delenv("UBAG_REGION", raising=False)
    monkeypatch.delenv("UBAG_WORKER_ROAMING", raising=False)
    r = reload_region()
    assert r.subject_filter() == "ubag.jobs.default.>"


def test_subject_filter_pinned_region(monkeypatch):
    monkeypatch.setenv("UBAG_REGION", "us-west-1")
    monkeypatch.delenv("UBAG_WORKER_ROAMING", raising=False)
    r = reload_region()
    assert r.subject_filter() == "ubag.jobs.us-west-1.>"


def test_subject_filter_roaming_widens(monkeypatch):
    monkeypatch.setenv("UBAG_REGION", "us-west-1")
    monkeypatch.setenv("UBAG_WORKER_ROAMING", "1")
    r = reload_region()
    assert r.subject_filter() == "ubag.jobs.>"


def test_current_region_empty_when_not_set(monkeypatch):
    monkeypatch.delenv("UBAG_REGION", raising=False)
    r = reload_region()
    assert r.current_region() == ""


def test_is_roaming_false_by_default(monkeypatch):
    monkeypatch.delenv("UBAG_WORKER_ROAMING", raising=False)
    r = reload_region()
    assert r.is_roaming() is False


def test_is_roaming_true_when_set(monkeypatch):
    monkeypatch.setenv("UBAG_WORKER_ROAMING", "1")
    r = reload_region()
    assert r.is_roaming() is True
