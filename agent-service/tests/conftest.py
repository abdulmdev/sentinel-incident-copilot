import pytest

import app.main as main
from app.auth import gateway_call


@pytest.fixture(autouse=True)
def trusted_gateway(request):
    """Most tests exercise the workflow, not transport auth: treat calls as coming from the gateway.
    test_auth.py opts out (marker `real_auth`) and proves the check itself."""
    if request.node.get_closest_marker("real_auth"):
        yield
        return
    main.app.dependency_overrides[gateway_call] = lambda: {}
    yield
    main.app.dependency_overrides.pop(gateway_call, None)
