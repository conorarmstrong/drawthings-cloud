# Changelog

All notable changes. Newest first. Dates are when the change was made.

## Unreleased (2026-11-18)

### Changed

- **One binary.** All AWS work now goes through the AWS SDK for Go instead of the `aws` CLI and shell scripts. `dt` plus `config.json` is the whole app. The `aws` CLI and `jq` are no longer needed.
- **Embedded assets.** The server boot script and the volume reaper Lambda live in `assets/` and are built into the binary.
- **Faster AWS calls.** There is no Python start-up per call. Checking every region, the S3 bucket, volumes and prices takes about 2 seconds.
- **Firewall sync.** A launch adds and removes only the SSH rules that changed, instead of removing all of them and adding them back.
- **Launch cancel.** `c` cancels the launch directly instead of through a file.
- **Diagnostics.** `g` now shows a busy state while it runs.
- **Log size.** The log keeps 5000 lines instead of 500.

### Added

- **Headline.** One line saying what is happening and what to do next, for example "LAUNCHING: trying g7e.2xlarge in us-east-2 at $1.82/h", "BOOTING 4/5", "READY" or "FAILED".
- **Cost beside the headline.** Region and zone, cost so far, hourly rate and time to idle shutdown, on every tab.
- **Context-aware key bar.** It lists only the keys that work right now and on the current tab.
- **Live launch table.** One row per instance type and region, with price, status and the failure reason.
- **Boot stepper and ETA.** Shows instance, setup, models, server, ready. The time left is the median of recent boots of the same kind, recorded in `.state/boots.jsonl`.
- **Log tab.** Scrolling, and a filter for all, this Mac, server or problems.
- **Confirmations in one place.** Every "press again" prompt appears in one red line above the key bar.
- **Ready sound.** The ready notification plays a sound.
- **`d d` on a server that is shutting down** force-detaches the model volume without launching.
- **`./dt -dry-launch`** runs the whole launch path while EC2 only validates the request.
- **Tests.** Zone hints, price order, boot script fill-in, the reaper zip, boot ETA, the key bar, the headline, log filtering and scrolling, and read-only live AWS checks.
- **Docs.** `README.md` with screenshots, `KNOWN_ISSUES.md` and this changelog.
- **Setup wizard.** With no `config.json`, `dt` asks for the bucket, looks up its region, lists the models folder to tick files from, then asks for regions and spot or on-demand.
- **`config.example.json`.** Every key with example values. `config.json` is now in `.gitignore`.
- **`~` in `local_models_dir`** means your home folder.

### Fixed

- **Cancel mid-request.** Cancelling during a `run-instances` call was logged as a zone failure.
- **Cancel cleanup.** The launch's cancel was never released after a launch finished.
- **Failed force-detach.** On a relaunch, a failed force-detach was silently ignored.
- **Public IP check.** Any HTTP reply was accepted. Only a 200 reply is accepted now.
- **Log lines.** Viewing the Log tab cut off long stored log lines for good.
- **Launch table status.** The table showed "trying" during the volume attach. It shows "launched" now.

### Removed

- Personal defaults: the bucket name and model list are no longer built into the code.

- `scripts/`: `up.sh`, `up_region.sh`, `setup.sh`, `reaper.sh`, `down.sh`, `free_volume.sh`, `volume.sh`, `diag.sh`, `common.sh`, the two CDN helper scripts, and `run.sh`.
- The fallback that read boot progress from the metrics feed for servers older than the event log.
- The old `dtaws` binary and stale state files.

## 2026-11-18

### Added

- **Multi-region launch.** Tries every region in `regions`, ordered by live spot or on-demand price.
- **Capacity handling.** Fallback instance types and a retry window with backoff (`fallback_instance_types`, `launch_retry_minutes`). It follows AWS's suggested zone after a capacity error.
- **Automatic region setup.** Sets up the security group, key pair, reaper and a default VPC when missing.
- **Region and zone** on the dashboard and live metrics panel.
- **Server event log.** `/var/log/dt-events.log` covers boot steps, the volume, each model file fetched, the image fetch, the container, connections, the watchdog and server log lines. It is streamed live into the TUI.
- **Verbose launch logging.** Every region checked, every price, every zone tried and every failure reason.
- **Cancel tracking.** Logs the GPU time and cost thrown away when a generation is cancelled.
- **Diagnostics on terminate.** Saved automatically before terminating.
- **Price regression test.** A live test for instance types and prices.

### Changed

- **Faster capacity failures.** One `run-instances` attempt per zone instead of the CLI's retries. A full region now fails in 10 to 20 seconds instead of about 50.
- **Model copy.** Runs three files at a time, with per-file logging.
- **Server image.** Fetched in parallel with the model copy.
- **Volume lookup.** Delete and status cover every configured region.

### Fixed

- **Prices.** Instance types were joined into one argument, so regions with several types returned no prices and sessions were recorded at $0.
- **Server lookup.** A Python warning on stderr from the `aws` CLI was mixed into JSON, causing intermittent `invalid character 'E'` errors.
- **Boot log.** The boot line was blank because IMDSv2 tokens were not used.
- **Metrics panel.** A malformed metrics line kept the panel stuck on "Waiting for SSH".
- **Small file sizes.** Files under 1 MiB were logged as "0 MiB".
- **Reaper policy.** It only allowed volumes in one region.
- **macOS bash 3.2.** Scripts used `declare -A` and GNU-only `sed -i`.

## 2026-11-17

### Added

- **First version.** A terminal app with Dashboard, Config, Models, Connect and Log tabs.
- **Launch.** Spot or on-demand launch of a Draw Things gRPC server, with models copied from S3.
- **Tunnel and metrics.** An SSH tunnel and a live metrics feed.
- **Model volume.** A persistent EBS model volume with a reaper Lambda.
- **Shutdown watchdog.** Shuts the server down on idle, maximum lifetime and boot timeout.
- **Costs and alerts.** Session cost history, a monthly budget warning and macOS notifications.
- **Who can connect.** Editing of allowed IPs, and an optional shared secret.
