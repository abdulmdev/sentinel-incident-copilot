"""The rule baseline is also the production fallback when the LLM is down, so its safety matters."""
from datetime import datetime, timedelta, timezone

import pytest

from app import heuristics
from app.models import Evidence, Incident

OPENED = datetime(2026, 10, 5, 10, 30, tzinfo=timezone.utc)


def incident():
    return Incident(id="i", tenant_id="t", service="checkout", summary="5xx", opened_at=OPENED)


def deploy(minutes_before_alert: float) -> Evidence:
    ts = (OPENED - timedelta(minutes=minutes_before_alert)).isoformat()
    return Evidence(source="deploys:checkout", detail=f"deployment completed: version 1.5.0 commit abc at {ts} (x)")


DB_POOL = Evidence(source="logs:checkout", detail="ERROR x40: timeout acquiring DB connection {}")


@pytest.mark.parametrize("minutes,suspect", [(5, True), (30, True), (31, False), (360, False), (-5, False)])
def test_deploy_suspect_window(minutes, suspect):
    rca = heuristics.rca(incident(), [deploy(minutes), DB_POOL])
    assert (rca.remediation.action == "rollback") is suspect


def test_old_deploy_never_triggers_a_rollback_of_a_healthy_build():
    rca = heuristics.rca(incident(), [deploy(6 * 60), DB_POOL])
    assert rca.remediation.action == "increase_pool"
    assert [c.cause for c in rca.root_causes] == ["db_pool_exhaustion"]
    assert heuristics.triage(incident(), [deploy(6 * 60), DB_POOL]).category == "resource"


def test_deploy_with_unparseable_time_stays_a_suspect():
    e = Evidence(source="deploys:checkout", detail="deployment completed: version 1.5.0 (time unknown)")
    assert heuristics.rca(incident(), [e]).remediation.action == "rollback"
