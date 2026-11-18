"""Deletes the Draw Things model volume after it has sat unattached too long.

Runs every 5 minutes. A detached volume is tagged with the time it was first
seen. It is deleted once it has been detached for DELETE_AFTER_MIN minutes.
A volume that gets attached again loses its tag, so the clock restarts.
"""
import os
import time

import boto3

ec2 = boto3.client("ec2")
TAG = os.environ["PROJECT_TAG"]
NAME = os.environ["VOLUME_NAME"]
LIMIT = int(os.environ.get("DELETE_AFTER_MIN", "0")) * 60
KEY = "AvailableSince"


def handler(event, context):
    if LIMIT <= 0:
        return {"disabled": True}
    now = int(time.time())
    out = {"tagged": [], "deleted": [], "reset": []}
    vols = ec2.describe_volumes(Filters=[
        {"Name": "tag:Project", "Values": [TAG]},
        {"Name": "tag:Name", "Values": [NAME]},
    ])["Volumes"]
    for v in vols:
        vid = v["VolumeId"]
        tags = {t["Key"]: t["Value"] for t in v.get("Tags", [])}
        if v["State"] == "in-use":
            if KEY in tags:
                ec2.delete_tags(Resources=[vid], Tags=[{"Key": KEY}])
                out["reset"].append(vid)
        elif v["State"] == "available":
            since = tags.get(KEY)
            if since is None:
                ec2.create_tags(Resources=[vid], Tags=[{"Key": KEY, "Value": str(now)}])
                out["tagged"].append(vid)
            elif now - int(since) >= LIMIT:
                ec2.delete_volume(VolumeId=vid)
                out["deleted"].append(vid)
    print(out)
    return out
