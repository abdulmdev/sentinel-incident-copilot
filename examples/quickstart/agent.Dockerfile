# The quickstart's demo agent: the Python SDK from this repository, and examples/quickstart/agent.py.
# Built from the repository root (see compose.yaml); agent.Dockerfile.dockerignore keeps the context small.
FROM python:3.13-slim
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1
COPY sdk/python /src/sdk
RUN pip install /src/sdk && rm -rf /src
COPY examples/quickstart/agent.py /app/agent.py
USER 65532:65532
ENTRYPOINT ["python", "/app/agent.py"]
