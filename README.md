# Spacelift Prometheus Exporter

This repository contains a Prometheus exporter for exporting metrics from your Spacelift account.

![Dashboard Example](dashboard-example.png)

## Quick Start

The Spacelift exporter is provided as a statically linked Go binary and a Docker container. You can
find the latest release [here](https://github.com/spacelift-io/prometheus-exporter/releases/latest).
The Docker container is available from our public container registry:
`public.ecr.aws/spacelift/promex`.

### Authentication

The exporter uses
[Spacelift API keys](https://docs.spacelift.io/integrations/api#spacelift-api-key-greater-than-token)
to authenticate, and also needs to know your Spacelift account API endpoint. Your API endpoint is in
the format `https://<account>.app.spacelift.io`, for example `https://my-account.app.spacelift.io`.

**NOTE:** on current Spacelift SaaS any API key works, provided it can read the root space (the
`usage` field requires it). Self-Hosted releases older than August 2026 restrict `publicWorkerPool`,
`usage` and `metrics` to non-machine keys; on such a release, run the exporter with an admin user's
key or with `--partial-scrapes`, which serves the private worker pool metrics and reports the
restricted collectors as unsupported (see below).

#### OIDC API keys with rotating secrets

If your API key is configured for OIDC (the key ID has the form `oidc::<issuer>::<key-id>`), the
"secret" is an OIDC JWT issued by your identity provider rather than a static string. Use
`--api-key-secret-file` (or `SPACELIFT_PROMEX_API_KEY_SECRET_FILE`) to point at the file that holds
the token. The file is re-read on every token refresh, so projected Kubernetes service-account
tokens that rotate on disk are picked up automatically without restarting the exporter.

### Running via the Binary

Download the exporter binary from our
[releases](https://github.com/spacelift-io/prometheus-exporter/releases/latest) page, make sure it's
added to your PATH, and then use the `spacelift-promex serve` command to run the exporter binary:

```shell
spacelift-promex serve --api-endpoint "https://<account>.app.spacelift.io" --api-key-id "<API Key ID>" --api-key-secret "<API Key Secret>"
```

### Running via Docker

Use the following command to run the exporter via Docker:

```shell
docker run -it --rm -p 9953:9953 -e "SPACELIFT_PROMEX_API_ENDPOINT=https://<account>.app.spacelift.io" \
  -e "SPACELIFT_PROMEX_API_KEY_ID=<API Key ID>" \
  -e "SPACELIFT_PROMEX_API_KEY_SECRET=<API Key Secret>" \
  public.ecr.aws/spacelift/promex
```

### Running in Kubernetes

You can use the following Deployment definition to run the exporter:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: spacelift-promex
  labels:
    app: spacelift-promex
spec:
  replicas: 1
  selector:
    matchLabels:
      app: spacelift-promex
  template:
    metadata:
      labels:
        app: spacelift-promex
    spec:
      containers:
        - name: spacelift-promex
          image: public.ecr.aws/spacelift/promex:latest
          ports:
            - name: metrics
              containerPort: 9953
          readinessProbe:
            httpGet:
              path: /health
              port: metrics
            periodSeconds: 5
          env:
            - name: "SPACELIFT_PROMEX_API_ENDPOINT"
              value: "" # Add your endpoint here
            - name: "SPACELIFT_PROMEX_API_KEY_ID"
              value: "" # Add your API key here
            - name: "SPACELIFT_PROMEX_API_KEY_SECRET"
              value: "" # Add your secret here
            - name: "SPACELIFT_PROMEX_LISTEN_ADDRESS"
              value: ":9953"
```

To use the example deployment, make sure you fill in the API endpoint, API Key ID and API Key
Secret, as explained in the comments. For a production deployment we would recommend making use of
Kubernetes secrets rather than embedding the API key values directly.

## Port Number

By default the exporter listens on port 9953. To change this use the `--listen-address` flag or the
`SPACELIFT_PROMEX_LISTEN_ADDRESS` environment variable:

```shell
spacelift-promex serve --listen-address ":9999" --api-endpoint "https://<account>.app.spacelift.io" --api-key-id "<API Key ID>" --api-key-secret "<API Key Secret>"
```

## Custom CA Certificate

If your Spacelift endpoint uses a certificate chain that is not trusted by system CAs, you can add
an extra trusted root certificate with `--ca-cert-path` or `SPACELIFT_PROMEX_CA_CERT_PATH`.
The exporter keeps system CAs and appends the certificate from the path you provide.

```shell
spacelift-promex serve --ca-cert-path "/certs/spacelift-ca.crt" --api-endpoint "https://<account>.app.spacelift.io" --api-key-id "<API Key ID>" --api-key-secret "<API Key Secret>"
```

## Help

To get information about all the available commands and options, use the `help` command:

```shell
$ spacelift-promex help
NAME:
   spacelift-promex - Exports metrics from your Spacelift account to Prometheus

USAGE:
   spacelift-promex [global options] command [command options] [arguments...]

VERSION:
   0.0.1

COMMANDS:
   serve    Starts the Prometheus exporter
   help, h  Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --help, -h     show help (default: false)
   --version, -v  print the version (default: false)


COPYRIGHT:
   Copyright (c) 2022 spacelift-io
```

To get information about an individual command, use the `--help` flag:

```shell
$ spacelift-promex serve --help
NAME:
   spacelift-promex serve - Starts the Prometheus exporter

USAGE:
   spacelift-promex serve [command options] [arguments...]

OPTIONS:
   --api-endpoint value, -e value    Your spacelift API endpoint (e.g. https://myaccount.app.spacelift.io) [$SPACELIFT_PROMEX_API_ENDPOINT]
   --ca-cert-path value              Path to a PEM-encoded CA certificate to trust in addition to system certificates [$SPACELIFT_PROMEX_CA_CERT_PATH]
   --api-key-id value, -k value      Your spacelift API key ID [$SPACELIFT_PROMEX_API_KEY_ID]
   --api-key-secret value, -s value  Your spacelift API key secret. Mutually exclusive with --api-key-secret-file. [$SPACELIFT_PROMEX_API_KEY_SECRET]
   --api-key-secret-file value       Path to a file containing the spacelift API key secret. The file is re-read on every token refresh, so this is the right choice for rotating secrets such as Kubernetes projected service-account tokens used with OIDC API keys. Mutually exclusive with --api-key-secret. [$SPACELIFT_PROMEX_API_KEY_SECRET_FILE]
   --is-development, -d              Uses settings appropriate during local development (default: false) [$SPACELIFT_PROMEX_IS_DEVELOPMENT]
   --listen-address value, -l value  The address to listen on for HTTP requests (default: ":9953") [$SPACELIFT_PROMEX_LISTEN_ADDRESS]
   --scrape-timeout value, -t value  The maximum duration to wait for a response from the Spacelift API during scraping (default: 5s) [$SPACELIFT_PROMEX_SCRAPE_TIMEOUT]
   --partial-scrapes                 Serve the metrics of the collectors that succeeded when another collector fails, instead of failing the whole scrape. A scrape in which no collector succeeds still returns HTTP 500 (default: false) [$SPACELIFT_PROMEX_PARTIAL_SCRAPES]
   --[no-]collector.publicworkerpool Enable the publicworkerpool collector (default: true) [$SPACELIFT_PROMEX_COLLECTOR_PUBLICWORKERPOOL]
   --[no-]collector.workerpools      Enable the workerpools collector (default: true) [$SPACELIFT_PROMEX_COLLECTOR_WORKERPOOLS]
   --[no-]collector.usage            Enable the usage collector (default: true) [$SPACELIFT_PROMEX_COLLECTOR_USAGE]
   --[no-]collector.aggregates       Enable the aggregates collector (default: true) [$SPACELIFT_PROMEX_COLLECTOR_AGGREGATES]
   --[no-]collector.workerstatus     Enable the workerstatus collector (default: false) [$SPACELIFT_PROMEX_COLLECTOR_WORKERSTATUS]
   --[no-]collector.stacks           Enable the stacks collector (default: false) [$SPACELIFT_PROMEX_COLLECTOR_STACKS]
```

## Version

To get version information, use the `--version` flag:

```shell
$ spacelift-promex --version
spacelift-promex version 0.0.1
```

## Scrape failures

Each scrape issues one GraphQL request per enabled collector, concurrently, within one
`--scrape-timeout` deadline. `publicworkerpool`, `workerpools`, `usage` and `aggregates` are on by
default; the [opt-in collectors](#opt-in-collectors) are off. Enable or disable a collector with
`--[no-]collector.<name>` or `SPACELIFT_PROMEX_COLLECTOR_<NAME>=true|false`; a disabled collector
issues no request and emits no series.

By default, an error from any collector fails the whole scrape: `/metrics` returns HTTP 500 and
Prometheus records the target as down, so existing `up == 0` alerts keep working. The response body
names the collector that failed.

With `--partial-scrapes` (or `SPACELIFT_PROMEX_PARTIAL_SCRAPES=true`), `/metrics` returns HTTP 200
with the metrics of the collectors that succeeded, and the scrape fails only when no collector
succeeds. Alert on the individual collectors instead of, or as well as, the target:

```promql
spacelift_scrape_collector_success == 0
```

`spacelift_scrape_collector_supported == 0` means the deployment, tier or API key cannot supply that
collector's data: the field is gated for API keys, or the backend's schema lacks a field the
collector declares as one older backends may not have. One missing declared field makes the whole
collector report `supported` 0, even when its other fields exist. Any other missing field, such as a
typo, is a failure (`success` 0). Either way the API's message naming the field is in the
strict-mode HTTP 500 body and the exporter log. An unsupported collector is not a failure, and in
partial mode it does not fail the scrape while any collector still succeeds.

## Available Metrics

The following metrics are provided by the exporter:

| Metric                                                     | Labels                               | Description                                                                                    |
| ---------------------------------------------------------- | ------------------------------------ | ---------------------------------------------------------------------------------------------- |
| `spacelift_public_worker_pool_runs_pending`                |                                      | The number of runs in your account currently queued and waiting for a public worker            |
| `spacelift_public_worker_pool_workers_busy`                |                                      | The number of currently busy workers in the public worker pool for this account                |
| `spacelift_public_worker_pool_parallelism`                 |                                      | The maximum number of simultaneously executing runs on the public worker pool for this account |
| `spacelift_worker_pool_runs_pending`                       | `worker_pool_id`, `worker_pool_name` | The number of runs currently queued and waiting for a worker from a particular pool            |
| `spacelift_worker_pool_workers_busy`                       | `worker_pool_id`, `worker_pool_name` | The number of currently busy workers in a worker pool                                          |
| `spacelift_worker_pool_workers`                            | `worker_pool_id`, `worker_pool_name` | The number of workers in a worker pool                                                         |
| `spacelift_worker_pool_workers_drained`                    | `worker_pool_id`, `worker_pool_name` | The number of workers in a worker pool that have been drained                                  |
| `spacelift_current_billing_period_start_timestamp_seconds` |                                      | The timestamp of the start of the current billing period                                       |
| `spacelift_current_billing_period_end_timestamp_seconds`   |                                      | The timestamp of the end of the current billing period                                         |
| `spacelift_current_billing_period_used_private_seconds`    |                                      | The amount of private worker usage in the current billing period                               |
| `spacelift_current_billing_period_used_public_seconds`     |                                      | The amount of public worker usage in the current billing period                                |
| `spacelift_current_billing_period_used_seats`              |                                      | The number of seats used in the current billing period                                         |
| `spacelift_current_stacks_count_by_state`                  | `state`                              | The number of stacks grouped by state                                                          |
| `spacelift_current_resources_count_by_drift`               | `state`                              | The number of resources by drift                                                               |
| `spacelift_current_avg_stack_size_by_resource_count`       |                                      | The average stack size by resource count                                                       |
| `spacelift_current_average_run_duration`                   |                                      | The average run duration                                                                       |
| `spacelift_current_median_run_duration`                    |                                      | The median run duration                                                                        |
| `spacelift_scrape_collector_success`                       | `collector`                          | Whether the collector succeeded on the last scrape (1) or failed (0)                           |
| `spacelift_scrape_collector_supported`                     | `collector`                          | Whether the collector's data is available on this deployment, tier and API key (1) or not (0) |
| `spacelift_scrape_collector_duration_seconds`              | `collector`                          | The duration in seconds of the collector's request to the Spacelift API on the last scrape     |
| `spacelift_scrape_duration_seconds`                        |                                      | The duration in seconds of the request to the Spacelift API for metrics (the whole scrape; collectors query concurrently) |
| `spacelift_build_info`                                     |                                      | Contains build information about the exporter (version, commit, etc)                           |

## Opt-in collectors

These collectors are off by default, so adding one to the exporter changes neither the documents
nor the series of the default scrape. Each one adds requests to every scrape, as the table below
lists. Enable them one at a time and watch
`spacelift_scrape_collector_duration_seconds{collector="<name>"}` and the series count.

| Collector      | Flag                       | Cost per scrape                                                                                                        |
| -------------- | -------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `workerstatus` | `--collector.workerstatus` | A second `workerPools` request that lists every worker of every pool, as `workerpools` does; 4 series per private pool |
| `stacks`       | `--collector.stacks`       | One `stacks` request that lists all stacks the API key can read; up to 5 series per stack (see [Sizing `stacks`](#sizing-stacks)) |

Run opt-in collectors with `--partial-scrapes`. Every collector shares one `--scrape-timeout`
deadline, and in the default strict mode one collector that fails or runs out of time fails the
whole scrape, taking the default metrics down with it. With partial scrapes it costs only its own
series, and `spacelift_scrape_collector_success{collector="<name>"}` says so.

| Metric                                    | Collector      | Labels                                         | Description                                                                                      |
| ----------------------------------------- | -------------- | ---------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `spacelift_worker_pool_workers_by_status` | `workerstatus` | `worker_pool_id`, `worker_pool_name`, `status` | Workers by status: `idle`, `busy`, `draining` (drained and busy) or `drained` (drained and idle) |
| `spacelift_stack_info` | `stacks` | `stack_id`, `stack_name`, `space_id` | Always 1; the stack's name and space, to join to the other stack series of the same target on `stack_id` |
| `spacelift_stack_state` | `stacks` | `stack_id`, `state` | Always 1, with the stack's current state as the API reports it, such as `FINISHED` or `FAILED` |
| `spacelift_stack_state_set_timestamp_seconds` | `stacks` | `stack_id` | When the stack entered its current state; absent when the API reports no time for the current state |
| `spacelift_stack_disabled` | `stacks` | `stack_id` | Whether the stack is disabled (1) or not (0) |
| `spacelift_stack_locked` | `stacks` | `stack_id` | Whether the stack is locked by a user (1) or not (0); the user is not exported |

`spacelift_worker_pool_workers_by_status` breaks the default pool series down by status.
`draining` plus `drained` normally equals `spacelift_worker_pool_workers_drained`, `busy` plus
`draining` normally equals `spacelift_worker_pool_workers_busy`, and the four statuses normally sum
to `spacelift_worker_pool_workers`. The two collectors send separate requests, so a worker that
registers or changes state between them can make these differ for a scrape.

```promql
# Pools with drained workers that are still finishing a run
spacelift_worker_pool_workers_by_status{status="draining"} > 0
```

### Sizing `stacks`

`stacks` is experimental: its metric names and labels may change in a later release.

Every scrape lists all stacks the API key can read, in one request whose response grows with the
number of stacks. The requests per day are 86,400 / scrape interval in seconds × the number of
scrapers, where a scraper is anything that requests `/metrics`: each replica of an HA Prometheus
pair, a second monitoring agent, or a probe. A 15s interval behind an HA pair is 11,520 full
listings a day, and a 60s interval from one scraper is 1,440.

The exporter answers `/metrics` only when every enabled collector has returned or the
`--scrape-timeout` deadline has passed, so a slow `stacks` request delays the default metrics too,
in partial mode as well as strict mode. Partial scrapes contain a collector's failure, not its
latency. Keep `--scrape-timeout` below the Prometheus job's `scrape_timeout`, which must not exceed
its `scrape_interval`. If Prometheus gives up first, it records the whole target as down, and the
abandoned request keeps running until `--scrape-timeout` expires.

On a large account, run `stacks` in a separate exporter instance with only that collector enabled,
scraped by its own job every 60s or slower, so its latency and failures cannot reach the default
metrics:

```shell
# Plus the usual API endpoint and key flags.
spacelift-promex serve --scrape-timeout=25s --collector.stacks \
  --no-collector.publicworkerpool --no-collector.workerpools \
  --no-collector.usage --no-collector.aggregates
```

```yaml
scrape_configs:
  - job_name: spacelift-stacks
    scrape_interval: 60s
    scrape_timeout: 30s
    static_configs:
      - targets: ["spacelift-promex-stacks:9953"]
```

Each stack has one `spacelift_stack_info`, `spacelift_stack_state`, `spacelift_stack_disabled` and
`spacelift_stack_locked` series, plus `spacelift_stack_state_set_timestamp_seconds` when the API
reports a time: up to 50,000 series for 10,000 stacks. The state is a label, so each state change a
scrape observes ends one `spacelift_stack_state` series and starts a new one, and Prometheus keeps
the ended series in its in-memory head block for a few hours. A stack that changes state often
therefore counts once for every state it has been seen in recently. Only `spacelift_stack_info`
carries the stack's name and space, so renaming a stack or moving it to another space replaces that
one series. Join it on `stack_id` and the target's `job` and `instance` to name the stack. Matching
on `stack_id` alone fails the whole query, with a duplicate-series error, as soon as two targets
expose the same stack:

```promql
spacelift_stack_state{state="FAILED"}
  * on (job, instance, stack_id) group_left (stack_name, space_id) spacelift_stack_info
```

Stack series are absent while the collector is failing: with `--partial-scrapes` and another
collector that succeeds, its families are missing from the scrape; otherwise the whole target is
down. An alert on a stack's state therefore resolves on every failed or timed-out `stacks` request
and fires again on the next good one. Hold it with `keep_firing_for` (Prometheus 2.42 or later), and
alert on the collector separately, through its health series or, when the target is down, `up`:

```yaml
groups:
  - name: spacelift-stacks
    rules:
      - alert: SpaceliftStackFailed
        expr: |
          spacelift_stack_state{state="FAILED"}
            * on (job, instance, stack_id) group_left (stack_name, space_id) spacelift_stack_info
        keep_firing_for: 10m
      - alert: SpaceliftStacksCollectorFailing
        expr: |
          spacelift_scrape_collector_success{collector="stacks"} == 0
            or up{job="spacelift-stacks"} == 0
        for: 5m
```

To check that the listing is complete, compare `count(spacelift_stack_state)` with
`sum(spacelift_current_stacks_count_by_state)` from the default `aggregates` collector. Both depend
on what the API key can read, and whether they always count the same stacks has not been verified.

## Example Dashboard

If you're looking for inspiration, you can find an example Grafana dashboard
[here](examples/example-dashboard.json).
