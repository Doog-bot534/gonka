# Mid-Epoch Participant Maintenance Windows

## Overview

This proposal introduces scheduled, block-height-based maintenance windows for participants. A maintenance window allows a participant to temporarily go offline mid-epoch without being penalized for downtime and without being expected to perform protocol or application-layer duties.

The goal is to support real operational maintenance on participant infrastructure without forcing operators to miss an entire epoch or rely on ad hoc downtime tolerance in liveness windows.

This proposal is intentionally designed as an exemption mechanism, not as a consensus-set membership churn mechanism. During maintenance, the participant remains part of the epoch structure, but liveness accounting and application duties are temporarily suspended for the scheduled interval.

---

## Motivation

Gonka epochs are long-lived, typically on the order of a day. That creates a practical problem for operators who need short maintenance windows on underlying machines. Typical maintenance tasks such as host reboots, kernel upgrades, disk work, hypervisor work, or planned networking changes often require downtime measured in minutes, not in whole epochs.

Without a mid-epoch maintenance feature, an operator currently has only bad options:

1. Stay online and defer necessary maintenance.
2. Go offline and risk protocol-level liveness penalties and application-level penalties.
3. Sit out an entire epoch, which is far too coarse for a 10-20 minute operational task.

The desired system should let participants schedule brief maintenance windows, go offline for that period, and return without being marked down for that scheduled downtime.

---

## Goals

1. Allow participants to schedule short maintenance windows inside an epoch.
2. Exempt scheduled maintenance from consensus downtime penalties.
3. Exempt scheduled maintenance from application-layer duties and penalties.
4. Preserve participant participation in epoch structure without removing the participant from epoch groups.
5. Bound safety risk by limiting concurrent maintenance windows through governance-controlled parameters.
6. Make all important operational policy knobs configurable by governance.

---

## Non-Goals

1. This proposal does not relax double-sign or evidence enforcement.
2. This proposal does not introduce a new admin-operated emergency pause mechanism.
3. This proposal does not attempt to redesign epoch formation or PoC scheduling.
4. This proposal does not require a new end-user UX beyond chain messages and queries.
5. This proposal does not, in this first version, solve analytics/reporting beyond normal logs and query endpoints.

---

## High-Level Design

The chain introduces scheduled maintenance reservations keyed by participant and block height range.

When a participant enters an active maintenance window:

1. Consensus liveness accounting is frozen for that participant.
2. Downtime-related jailing/slashing from missed signatures is not triggered for that participant during the active window.
3. The participant remains in epoch groups and is not removed from epoch structure.
4. The participant stops receiving new inference assignments immediately.
5. Application-layer penalties related to being unavailable are waived for the duration of the window.
6. When the window ends, normal liveness accounting and application duties resume immediately.

This design keeps the consensus set stable and avoids repeated mid-epoch removal and re-addition of participants for very short operational windows.

---

## User-Facing Semantics

### What Maintenance Exempts

During an active maintenance window, the participant is exempt from:

1. Consensus downtime penalties derived from missed signatures.
2. Application-layer service duties.
3. Application-layer downtime and expiry penalties related to being unavailable.
4. Random inference assignment.
5. Confirmation PoC duties.
6. Validation duties.

### What Maintenance Does Not Exempt

During an active maintenance window, the participant is still subject to:

1. Double-sign enforcement.
2. Evidence-based enforcement.
3. Any non-downtime protocol rule unrelated to scheduled maintenance.

### Rewards and Credit During Maintenance

Under the current proposal, maintenance does not pause rewards or maintenance-credit earning. Participants may continue to receive the rewards they would otherwise receive under the protocol, and successful epoch-based maintenance-credit earning remains in place.

This is an explicit policy choice and not an implementation accident. It is called out again in Open Issues because it may create incentives to maximize maintenance usage.

---

## Scheduling Semantics

Maintenance windows are block-height-based.

Each reservation specifies:

1. Participant identity.
2. Start block height.
3. Duration in blocks.

The window becomes active exactly at `start_height` and remains active through `start_height + duration_blocks - 1`.

Maintenance windows:

1. Must be scheduled sufficiently far in advance, according to a governance-controlled lead-time parameter.
2. May be canceled before activation.
3. May not be extended once active.
4. Are bounded by a governance-controlled maximum duration.

If a participant is already down, marked down, or jailed before the window begins, scheduling maintenance does not repair or alter that status. The maintenance reservation exists, but it does not undo existing protocol state.

---

## Credit Model

Maintenance credit is measured in blocks.

Participants accumulate maintenance credit over time and spend that credit when scheduling a maintenance window. Credit policy is governance-controlled.

This proposal assumes:

1. Credit is capped.
2. Credit is earned per successful epoch.
3. The earn amount per successful epoch is parameterized.
4. The maximum stored credit is parameterized.

For this proposal, a successful epoch is defined as an epoch for which the participant would normally receive epoch rewards.

Maintenance credit should be granted in the reward-claim flow for that epoch. In other words, when a participant successfully claims normal epoch rewards, the participant also receives the configured maintenance-credit allotment for that epoch.

All participants begin with zero maintenance credit when the feature is introduced. No retroactive credit is granted for epochs completed before the feature exists.

---

## Concurrency and Safety Limits

Concurrent maintenance must be limited so that the network does not lose too much active availability at once.

This proposal includes two independent governance-controlled caps:

1. A cap on the number of participants concurrently in maintenance.
2. A cap on the total consensus power concurrently in maintenance.

Both caps must be satisfied for a new reservation to be accepted.

The count cap improves operator predictability and scheduling simplicity.

The power cap protects network safety and avoids a situation where a small number of large participants consume too much concurrent maintenance budget.

This proposal does not introduce a separate degraded-mode or outage-mode scheduler.

---

## Protocol-Level Changes

### Consensus Liveness Exemption

The key protocol change is in consensus downtime accounting.

When a participant has an active maintenance window at the current block height:

1. Missed-signature liveness accounting is frozen for that participant.
2. Missed signatures during the maintenance window do not advance missed-block counters or bitmaps.
3. Downtime-related jailing and downtime-related slashing are not triggered from liveness handling for that participant.

This exemption applies only to scheduled maintenance and only to downtime-style liveness enforcement.

Double-sign and evidence paths remain unchanged.

### Resume Behavior

When the maintenance window ends:

1. Liveness accounting resumes immediately at the next block.
2. The participant is evaluated from its pre-maintenance liveness state, as though accounting was paused during the maintenance interval.
3. If the participant remains offline after maintenance ends, normal markdown / missed-signature accumulation resumes and the participant must bear the consequences.

---

## Application-Level Changes

### Immediate Assignment Exclusion

At the start of a maintenance window, the participant must stop receiving new random inference assignments immediately.

The participant remains in epoch groups, but assignment logic must treat the participant as temporarily unavailable for the duration of the maintenance window.

The same exemption applies to other application-layer duties, including:

1. Confirmation PoC duties.
2. Validation duties.

### Penalty Exemptions

During an active maintenance window:

1. Downtime-related application penalties are waived.
2. Expiry-related penalties caused by participant unavailability are waived.
3. Participant-status degradation caused solely by maintenance-covered unavailability is suppressed.

### Epoch Group Membership

Participants in maintenance are not removed from epoch groups and are not removed from the epoch’s structural state. The proposal intentionally keeps epoch structure stable and only changes active service expectations.

---

## State

The chain should store, at minimum, the following maintenance-related state.

### Maintenance Reservation

Each reservation record should include:

1. Participant identity.
2. Start block height.
3. Duration in blocks.
4. Creator / scheduler identity.
5. Reservation status.

Suggested statuses:

1. Scheduled
2. Active
3. Completed
4. Canceled

### Maintenance Credit Balance

Maintenance credit should be stored on the participant record itself as a field measured in blocks.

This proposal prefers extending the existing participant record over introducing a separate maintenance-credit table.

### Indexing

The implementation should support efficient lookup by:

1. Participant
2. Current height
3. Scheduled future windows

This is necessary for both slashing-path checks and query endpoints.

---

## Messages

### `MsgScheduleMaintenance`

Schedules a maintenance window for a participant.

Fields should include:

1. Participant identity.
2. Start block height.
3. Duration in blocks.

Validation should include:

1. Caller is the participant or an authorized delegate via AuthZ.
2. Duration is positive.
3. Duration does not exceed the configured maximum.
4. Start height satisfies the minimum scheduling lead time.
5. Participant has enough maintenance credit.
6. Concurrent-count and concurrent-power caps are satisfied.
7. Reservation does not overlap another active or scheduled reservation for the same participant.

### `MsgCancelMaintenance`

Cancels a not-yet-active maintenance window.

Validation should include:

1. Caller is the participant or an authorized delegate via AuthZ.
2. Reservation is still in scheduled state.
3. Cancellation satisfies any configured cancellation policy.

Canceled windows restore the unspent maintenance credit associated with the reservation.

### Authorization

Maintenance scheduling is not treated as a monetary operation. Authorized delegates should be able to schedule and cancel maintenance on behalf of the participant, using the chain’s existing AuthZ mechanism.

---

## Queries

The chain should expose dedicated query endpoints for maintenance state.

Recommended queries:

1. Current maintenance-credit balance for a participant.
2. Scheduled maintenance windows for a participant.
3. Active maintenance windows.
4. Maintenance status for a participant at the current height.
5. Concurrent reserved participant count and reserved power at a height.
6. A scheduling-availability query that answers whether a maintenance window could be scheduled for a proposed participant, start height, and duration.

The scheduling-availability query is important operationally. Participants should be able to query whether a requested maintenance window is currently schedulable before constructing and broadcasting `MsgScheduleMaintenance`, so they can avoid wasting gas on a request that would be rejected.

Normal parameter queries remain sufficient for governance-controlled policy values.

---

## Parameters

This proposal introduces a new maintenance-window parameter group inside the global parameter set, controlled by governance.

Suggested parameters:

| Parameter | Description |
|-----------|-------------|
| `maintenance_enabled` | Enables or disables scheduling and activation of maintenance windows. |
| `maintenance_min_schedule_lead_blocks` | Minimum number of blocks between scheduling and start. |
| `maintenance_max_window_blocks` | Maximum duration of a single maintenance window. |
| `maintenance_max_concurrent_validators` | Maximum number of participants concurrently in maintenance. |
| `maintenance_max_concurrent_power_bps` | Maximum consensus power concurrently in maintenance. |
| `maintenance_credit_cap_blocks` | Maximum maintenance credit a participant may accumulate. |
| `maintenance_credit_earn_per_successful_epoch_blocks` | Number of credit blocks earned per successful epoch. |

Additional maintenance-related parameters may be added if implementation reveals a need for more explicit policy control.

---

## Reservation Acceptance Rules

`MsgScheduleMaintenance` should be accepted only if both of the following checks succeed against existing scheduled reservations:

1. The reservation does not cause the number of concurrent maintenance participants to exceed the configured count cap.
2. The reservation does not cause the total concurrent maintenance power to exceed the configured power cap.

The first version should evaluate concurrency only at scheduling time, not at activation time. This choice favors determinism and operator predictability.

This creates one acknowledged limitation: power-based concurrency is evaluated using the participant power known at the time of scheduling, not the participant power that may exist at activation time. If a reservation is scheduled far in advance and participant power changes materially before activation, concurrency estimates may be inaccurate.

This is a possible attack surface or policy edge case, but it is not expected to be a major issue in the first version and should be called out explicitly for later review.

The same logic should be exposed through the scheduling-availability query so operators can preflight candidate windows before sending a transaction.

---

## Logging and Observability

The implementation should emit standard structured logs through the existing logging framework for:

1. Maintenance scheduled.
2. Maintenance canceled.
3. Maintenance activated.
4. Maintenance completed.
5. Consensus liveness exemption applied.
6. Application-layer assignment suppression applied.
7. Application-layer penalty waiver applied.

This proposal does not require additional analytics/reporting systems in the first version.

---

## Implementation Notes

### Primary Enforcement Point

This feature must be implemented primarily in the protocol liveness path, not only in application hooks.

The decisive protocol behavior is:

1. Freeze missed-signature accounting during active maintenance.
2. Skip downtime-driven jailing/slashing during active maintenance.

Application-layer changes then mirror that protocol exemption:

1. No new inference assignments during maintenance.
2. No maintenance-covered expiry or downtime penalties.

### Hooks as Defense in Depth

Existing collateral or staking hooks may still be updated as a secondary guardrail so that downtime-derived side effects are not accidentally applied to maintenance-covered participants. However, hook-only logic is not sufficient for this feature.

The reason is that even if hook logic avoids collateral consequences, the participant can still be jailed by the underlying consensus-liveness logic if the primary slashing path is not patched. Hooks are therefore defense in depth, not the primary enforcement point.

### Cross-Repository Implementation Work

This feature will require implementation work in at least two places:

1. The maintained Cosmos SDK fork, for protocol-level liveness exemption behavior.
2. The core inference-chain codebase, for maintenance state, assignment suppression, application-duty exemptions, and credit accounting.

---

## Risks and Tradeoffs

### Safety Risk from Overlapping Maintenance

If too many participants are simultaneously in maintenance, the network may lose too much availability even if no one is penalized. This is why the proposal includes both participant-count and participant-power caps.

### Policy Risk from Preserved Rewards

If maintenance preserves rewards and credit earning, participants may be incentivized to maximize maintenance usage. This may or may not be acceptable depending on how short the permitted windows are and how much maintenance credit can be accumulated.

### Complexity in Duty Suspension

Removing participants from new assignment while keeping them in epoch groups is intentionally lighter-weight than structural epoch mutation, but it requires careful handling in assignment code and penalty code.

### Testing and End-to-End Validation Risk

This feature requires convincing end-to-end testing, not only unit testing.

In particular, the Testermint framework will need to support:

1. Pausing participant execution for an active maintenance window.
2. Verifying that the participant is not jailed as a result of the scheduled downtime.
3. Verifying that the participant does not receive maintenance-covered assignments or duties.
4. Verifying that the participant resumes normal behavior after the window ends.

This is a non-trivial testing requirement and should be treated as a significant implementation risk. The feature is not trustworthy without end-to-end validation of the full pause / exemption / resume flow.

---

## Open Issues

### 1. In-Flight Inferences

The proposal requires that participants stop receiving new random assignments immediately when maintenance begins. However, some inferences may already be in flight when the window starts.

This proposal intentionally leaves the exact treatment of in-flight work as an open issue.

Questions to resolve:

1. Are in-flight inferences explicitly canceled when maintenance begins?
2. Are they allowed to continue serving as a transitional state before going fully offline?
3. Are there request classes that should be treated differently from others?

Current direction:

1. New assignments stop immediately.
2. Penalties for work disrupted by the maintenance window are waived.
3. Exact handling of in-flight requests needs explicit design and may require follow-up work.

### 2. Incentive to Maximize Maintenance Usage

Because the current proposal preserves rewards and maintenance-credit earning during maintenance, participants may be incentivized to maximize maintenance usage whenever possible.

This may be partially mitigated if:

1. Maximum single-window duration remains short.
2. Credit accumulation is bounded.
3. Concurrent maintenance is tightly capped.

Even so, the incentive question should be treated as an open policy issue and explicitly reviewed before final implementation.

### 3. Subnet Interaction

Subnet functionality is still under development, and this proposal does not yet specify how maintenance windows interact with subnet-specific duties, scheduling, or availability assumptions.

This should be treated as an explicit open issue so that subnet work does not accidentally bake in assumptions that conflict with maintenance exemptions.

### 4. Reservation Pruning

Completed and canceled maintenance reservations will accumulate over time unless they are pruned.

This proposal does not require immediate pruning logic, because reservation volume is not expected to be large in the first version. However, maintenance reservation pruning should be kept as a nice-to-have follow-up item.

---

## Outcome

This proposal adds a practical operational capability that long-epoch networks need: short, scheduled, mid-epoch maintenance windows. The design avoids consensus-set churn, preserves epoch structure, freezes consensus liveness accounting during scheduled downtime, and suppresses application duties during the maintenance interval.

The remaining unresolved questions are narrow and explicit:

1. How to handle in-flight inferences when maintenance begins.
2. Whether preserving rewards and credit earning creates unacceptable incentives to maximize maintenance usage.
3. How maintenance windows should interact with subnet behavior.

With those issues called out, this proposal is ready for technical review and refinement into an implementation plan.
