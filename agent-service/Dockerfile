FROM python:3.12-slim
WORKDIR /app
RUN pip install --no-cache-dir uv
COPY pyproject.toml uv.lock* ./
RUN uv sync --no-dev --no-install-project
COPY app ./app
RUN useradd --system --uid 10001 sentinel
USER 10001
EXPOSE 8000
# Run the venv directly: no uv (and no writable cache dir) needed at runtime, so a read-only root FS works.
CMD ["/app/.venv/bin/uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000"]
