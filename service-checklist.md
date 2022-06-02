# Documentation & Understanding

- [X] Is your service/feature's documentation linked from Cerebro?
- [X] Does your service/feature have an architecture diagram which is accurate and current?
- [ ] Do you review capacity concerns for your service/feature at least every 3 months? N/A - Project is deployed as a sidecar, with varying requirements for the consuming applications

# Configuration Management

<details>
  <summary>Arturos not applicable to this project</summary>

- [ ] Are the effects of Arturo features documented in the Arturo page (or linked from it)?
- [ ] For long term Arturos, does your end-to-end and automated testing cover both pathways of the Arturo?
- [ ] Have all unused/deprecated configuration options (Arturos/settings, etc) been removed from your owned features/services?

</details>

# Confidence in Change

- [X] Are your core features tested?
- [ ] Are there automated acceptance tests in staging for your service? - N/A acceptance tests through consuming applications
- [ ] Are there automated smoke tests in production for your service? N/A smoke tests through consuming applications
- [ ] Do you gate stage progression for production deployments based on smoke test outcomes? N/A - Depends on consuming application rollout
- [ ] Do you have sufficient acceptance test coverage of your service's functionality? N/A - covered by core test functionality

# Early Detection & Response

<details>
  <summary>Monitoring is coupled with the consuming applications</summary>

- [ ] Are all monitors for this service/feature documented?
- [ ] Do all service/feature monitors link to a Runbook, or provide an escalation policy within them?
- [ ] Do you have monitoring and alerting that follows the [REDS pattern](https://phd.zende.sk/services?products=Guide)?
- [ ] Have you reviewed your alert thresholds in the last 90 days?
- [ ] Is your DEPLOY.md accurate?
- [ ] Is a Datadog [service deploy dashboard](https://zendesk.datadoghq.com/dashboard/4x8-36h-rst/help-center-status-overview?from_ts=1648641337855&to_ts=1648641637855&live=true) linked in Cerebro?
- [ ] Can you rollback a change made in your service/feature in less than 15 minutes?
- [ ] Does your service's Cerebro page provide a link to live logs/APM?
- [ ] Do you have good signal-to-noise ratio on your monitors and APM?
- [ ] Do your error capture mechanisms have good signal-to-noise ratio?
- [ ] Do you have a documented console role that can be used for running backfills and debugging production issues?

</details>

# On-Call

- [X] Do you know how to quickly detect problems with your service/feature dependencies?
- [X] Are your upstream/downstream dependencies linked in Cerebro under the "Uses" field?
- [X] Do you know how to page your upstream/downstream dependency on-call groups?
