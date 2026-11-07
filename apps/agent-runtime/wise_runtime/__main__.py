from __future__ import annotations

import json
import logging
import sys

from .config import Config
from .worker import Worker


class JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        data = {
            "time": self.formatTime(record),
            "level": record.levelname,
            "logger": record.name,
            "msg": record.getMessage(),
        }
        for key in ("run_id", "agent", "attempt"):
            if hasattr(record, key):
                data[key] = getattr(record, key)
        if record.exc_info:
            data["exc"] = self.formatException(record.exc_info)
        return json.dumps(data)


def main() -> None:
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(JsonFormatter())
    logging.basicConfig(level=logging.INFO, handlers=[handler])
    logging.getLogger("httpx").setLevel(logging.WARNING)
    config = Config.from_env()
    logging.getLogger("wise_runtime").info("runtime starting as %s → %s", config.runtime_id, config.api_url)
    Worker(config).run_forever()


if __name__ == "__main__":
    main()
