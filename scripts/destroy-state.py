#!/usr/bin/env python3
"""Select legacy SQL children to forget only during a full lab instance teardown."""
import json
import sys


def sql_children_to_forget(state, project, prefix):
    resources = {
        f'{r["type"]}.{r["name"]}': r
        for r in state.get("resources", [])
        if r.get("module") == "module.database" and r.get("mode", "managed") == "managed"
    }
    expected = {
        "google_sql_database.ticketing": "ticketing",
        "google_sql_user.app": "ticketing_app",
        "google_sql_user.observer": "lab_observer",
    }
    children = [name for name in expected if resources.get(name, {}).get("instances")]
    if not children:
        return []
    parents = resources.get("google_sql_database_instance.main", {}).get("instances", [])
    if len(parents) != 1:
        raise ValueError("Refusing to forget SQL children without their tracked lab instance")
    parent = parents[0]["attributes"]
    if parent.get("project") != project or parent.get("name") != f"{prefix}-pg":
        raise ValueError("SQL instance does not match the authorized project and lab name")
    settings = parent.get("settings", [])
    if (parent.get("deletion_protection") or parent.get("deletion_policy", "DELETE") != "DELETE"
            or any(s.get("deletion_protection_enabled") for s in settings)):
        raise ValueError("SQL instance has deletion protection; refusing to forget its children")
    addresses = []
    for name in children:
        instances = resources[name]["instances"]
        if len(instances) != 1 or instances[0].get("index_key") is not None:
            raise ValueError("Unexpected indexed SQL child in lab state")
        child = instances[0]["attributes"]
        if (child.get("project") != project or child.get("instance") != parent["name"]
                or child.get("name") != expected[name]
                or child.get("deletion_policy") == "PREVENT"):
            raise ValueError("SQL child is protected or does not belong to the tracked lab instance")
        addresses.append(f"module.database.{name}")
    return addresses


if __name__ == "__main__":
    try:
        for address in sql_children_to_forget(json.load(sys.stdin), sys.argv[1], sys.argv[2]):
            print(address)
    except (ValueError, KeyError, IndexError) as error:
        sys.exit(str(error))
