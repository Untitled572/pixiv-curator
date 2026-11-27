"""Prune the previous deployment backup after the new deployment is verified."""

import json
import re
import shutil
import sys
from pathlib import Path


def prune(new_backup):
    new_backup = new_backup.resolve(strict=True)
    backups = new_backup.parent
    if backups.name != "backups":
        raise ValueError("expected a deployment backup under backups")
    previous = json.loads((new_backup / "deployment.json").read_bytes())
    current = json.loads((backups.parent / "deployment.json").read_bytes())
    name = previous["backup"]
    pattern = r"pre-" + re.escape(previous["version"]) + r"-\d{8}T\d{6}Z"
    if current["backup"] != new_backup.name or name == new_backup.name:
        raise ValueError("the new backup is not registered as current")
    if not re.fullmatch(pattern, name) or "/" in name or "\\" in name:
        raise ValueError("invalid previous deployment backup name")
    prior_backup = backups / name
    if prior_backup.is_symlink():
        raise ValueError("previous backup is a symbolic link")
    if prior_backup.is_dir():
        shutil.rmtree(prior_backup)
        print(f"Pruned previous deployment backup: {name}")


if __name__ == "__main__":
    prune(Path(sys.argv[1]))
