# syntax=docker/dockerfile:1
ARG PYTHON_VERSION=3.13-slim

FROM python:${PYTHON_VERSION}
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_DISABLE_PIP_VERSION_CHECK=1
# ripgrep powers the search_code tool.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ripgrep \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --no-create-home wise
WORKDIR /app
COPY apps/agent-runtime/pyproject.toml ./
COPY apps/agent-runtime/wise_runtime ./wise_runtime
RUN pip install --no-cache-dir .
USER 10001
CMD ["python", "-m", "wise_runtime"]
