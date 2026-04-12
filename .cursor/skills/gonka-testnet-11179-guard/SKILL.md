---
name: gonka-testnet-11179-guard
description: Work with the Gonka testnet server at 89.169.111.79 and its deployment files. Use when the user mentions testnet, 89.169.111.79, ssh ubuntu@89.169.111.79, or asks to inspect, copy, deploy, or edit files on that host. Treat remote access as approval-gated: do not take any action on the server unless the user explicitly approves it.
---

# Gonka Testnet 111.79 Guard

## Scope

This skill is for the Gonka testnet host:

- Host: `ubuntu@89.169.111.79`
- Intended chain: `gonka-testnet-2`
- Join deployment directory: `/srv/dai/gonka/deploy/join`

Known services on this host may include:

- `node`
- `api`
- `proxy`
- `bridge`
- `explorer`
- `tmkms`

## Approval Gate

Treat this server as locked down.

Do not do any remote action unless the user explicitly approves it first.

Remote actions include:

- `ssh` commands
- `scp` or `rsync`
- copying files to or from the host
- editing remote files
- `docker` commands run on the host
- starting, stopping, restarting, or redeploying services
- reading secrets or env files on the host

If the user asks for work on this host but has not approved the action yet:

1. Stop.
2. State the exact action you want to take.
3. Ask for explicit approval before doing it.

If approval is ambiguous, ask again. Do not infer approval from context.

## Host Discipline

When using this skill:

1. Operate only on `89.169.111.79` unless the user explicitly switches hosts.
2. If a previous message mentioned another server, ignore it unless the user re-approves that host.
3. Before any remote command, restate the target host so mistakes are obvious.

## Default Workflow

1. Confirm the task is for `ubuntu@89.169.111.79`.
2. List the exact remote action you plan to take.
3. Ask for approval if it has not already been given.
4. After approval, execute only the approved action.
5. Report exactly what changed on the host.

## Examples

If the user says:

- "Check the containers on 89.169.111.79"

Reply first with something like:

> Planned action on `ubuntu@89.169.111.79`: run `docker ps` over SSH. Please confirm before I execute it.

If the user says:

- "Copy `deploy/join/docker-compose.subnetctl.yml` to 89.169.111.79"

That is explicit approval to copy that file to that host. Do only that approved copy unless the user also approves follow-up actions.
