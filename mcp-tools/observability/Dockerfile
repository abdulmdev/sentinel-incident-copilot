FROM python:3.12-slim
WORKDIR /app
RUN pip install --no-cache-dir uv
COPY pyproject.toml uv.lock* ./
RUN uv sync --no-dev --no-install-project
COPY server.py gateway_keys.py ./
ENV HOST=0.0.0.0 PORT=8200
RUN useradd --system --uid 10001 sentinel
USER 10001
EXPOSE 8200
CMD ["/app/.venv/bin/python", "server.py"]
