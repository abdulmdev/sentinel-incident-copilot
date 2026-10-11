"""The agent accepts exactly what the gateway sends (see contracts/README.md)."""
import json
import pathlib

from app.models import Incident

CONTRACTS = pathlib.Path(__file__).resolve().parents[2] / "contracts"


def test_gateway_open_incident_request_parses():
    payload = json.loads((CONTRACTS / "agent-open-incident.request.json").read_text())
    incident = Incident.model_validate(payload)
    assert set(payload) == set(Incident.model_fields)  # no field silently ignored or missing
    assert incident.tenant_id == "acme"
