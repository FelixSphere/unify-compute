"""Terminates GPU workers that outlived any job that could be running on them.

compute-api tears every attempt down with SkyPilot's /down, and SkyPilot's
autodown is meant to back that up. Neither runs if the compute host is down,
if SkyPilot's state is lost, or if the worker role (which is denied
everything) cannot autodown its own node. This runs outside the host, on a
schedule, and needs nothing from either.

A worker is reaped when it carries this environment's worker tags and was
launched longer ago than the platform's maximum runtime plus a margin. No job
can legitimately still be on it: max_runtime_seconds is capped by
compute-api/config/platform.json (upstreams.md R9; 4 hours since ADR-0010's
"Start small"), compute-api ends the attempt there, and MAX_AGE_HOURS is
derived from that same file.
"""

import datetime
import json
import os

import boto3

ec2 = boto3.client("ec2")
sns = boto3.client("sns")

PROJECT = os.environ["PROJECT"]
ENVIRONMENT = os.environ["ENVIRONMENT"]
MAX_AGE = datetime.timedelta(hours=float(os.environ["MAX_AGE_HOURS"]))
MAX_CONCURRENT = int(os.environ["MAX_CONCURRENT"])
TOPIC_ARN = os.environ["TOPIC_ARN"]


def workers():
    pages = ec2.get_paginator("describe_instances").paginate(
        Filters=[
            {"Name": "tag:Project", "Values": [PROJECT]},
            {"Name": "tag:Environment", "Values": [ENVIRONMENT]},
            {"Name": "instance-state-name", "Values": ["pending", "running", "stopping", "stopped"]},
        ]
    )
    for page in pages:
        for reservation in page["Reservations"]:
            yield from reservation["Instances"]


def handler(event, context):
    now = datetime.datetime.now(datetime.timezone.utc)
    found = []
    for i in workers():
        tags = {t["Key"]: t["Value"] for t in i.get("Tags", [])}
        found.append(
            {
                "id": i["InstanceId"],
                "type": i["InstanceType"],
                "state": i["State"]["Name"],
                "launched": i["LaunchTime"].isoformat(),
                "age_hours": round((now - i["LaunchTime"]).total_seconds() / 3600, 2),
                "cluster": tags.get("skypilot-cluster-name", tags.get("Name", "")),
                "expired": now - i["LaunchTime"] > MAX_AGE,
            }
        )

    expired = [w["id"] for w in found if w["expired"]]
    terminated, error = [], None
    if expired:
        try:
            ec2.terminate_instances(InstanceIds=expired)
            terminated = expired
        except Exception as e:  # reported below; the next run tries again
            error = str(e)

    report = {
        "environment": ENVIRONMENT,
        "at": now.isoformat(),
        "max_age_hours": MAX_AGE.total_seconds() / 3600,
        "workers": found,
        "terminated": terminated,
        "error": error,
        "over_concurrency_limit": len(found) > MAX_CONCURRENT,
    }
    print(json.dumps(report))

    if terminated or error or report["over_concurrency_limit"]:
        reasons = []
        if terminated:
            reasons.append(f"terminated {len(terminated)} worker(s) older than {report['max_age_hours']}h")
        if error:
            reasons.append("termination failed")
        if report["over_concurrency_limit"]:
            reasons.append(f"{len(found)} workers running, limit {MAX_CONCURRENT}")
        sns.publish(
            TopicArn=TOPIC_ARN,
            Subject=f"unify-compute {ENVIRONMENT} GPU reaper: {'; '.join(reasons)}"[:100],
            Message=json.dumps(report, indent=2),
        )
    return report
