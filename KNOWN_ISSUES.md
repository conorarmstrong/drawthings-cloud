# Known issues

Open problems and trade-offs, most important first. Each says what happens and what to do about it.

## Not yet proven live

The move from shell scripts to the AWS SDK for Go was checked with read-only calls and a dry-run launch.
These paths have unit tests, or ran in their earlier shell form, but have not run end to end through the SDK yet:

- a real launch, including the model volume create and attach
- model sets: the filtered S3 sync in the boot script, and the combined price ranking of instance types across regions
- terminate, force-detach and volume delete
- diagnostics and model upload
- first-time setup of a new region, including IAM and the reaper Lambda

Watch the Log tab on the first run of each. `./dt -dry-launch` checks the launch request without starting anything.

## Launch

- **A cached security group can go stale.** Region setup is skipped while `.state/sg_id.<region>` exists. If the group is deleted in the console, every launch there fails with `InvalidGroup.NotFound`. Delete the file and launch again.
- **Every launch resets SSH access to `allowed_cidrs`.** IPs added with `i` or `a` are removed at the next launch. Put permanent IPs in `config.json`.
- **All offered regions are set up at once.** On the first launch after adding regions, every region that offers the type gets a security group, key pair and reaper. This includes expensive regions that may never be tried.
- **The volume attach holds the launch open.** After the instance starts, `dt` waits for it to run and attaches the model volume. If the volume is still held by an old server, it retries for up to 10 minutes. `c` does nothing in that phase. If the attach fails, the server copies the models to its root disk, with only a warning in the log.
- **The dry run does not check capacity.** EC2 validates permissions and parameters only.
- **IPv6 rules are ignored.** The firewall sync only reads and removes IPv4 rules.

## Boot speed

- **The S3 image cache was slower than Docker Hub in one measurement.** On a warm boot, loading the cached server image took about 110 seconds, while a Docker Hub pull took about 43. That is one sample each. Setting `image_cache` to `false` may cut warm boots from about 2 minutes to about 1.
- **Cold boots copy every model in the active set.** A new zone, or a zone whose volume the reaper deleted, copies those models from S3. One measured cold boot was ready in 135 seconds, copying three files at a time at 220 to 290 MiB/s each.
- **Servers outside the bucket's region pay transfer.** A cold boot in another region is charged per GB for cross-region S3 transfer, and can be slower.

## Model sets and ranking

- **The volume keeps files from earlier sets.** The sync only adds. Using both `ltx` and `minimax` needs a volume big enough for both, or a fresh volume per switch.
- **Cheapest can mean a cold boot.** Instance types and regions are ranked by price alone. The cheapest pair may be in a zone with no model volume, which costs a full copy from S3.
- **A config saved by a running `dt` overwrites file edits.** Edit `config.json` with `dt` closed, or restart it afterwards.

## Costs shown are estimates

- **Session cost** is the price at the last poll times the hours run. Spot prices move during a session.
- **A session can be recorded as $0** if no price was known when it ended. The log says when this happens. Several sessions from before 2026-11-18 have $0 for this reason.
- **The model volume cost** uses London gp3 prices for every region.
- **On-demand prices** need the region in `pricing_locations`. Regions missing from it show no on-demand price.

## Monitoring is inferred

- **Jobs, stages and model loads are guessed from metrics** such as GPU use, VRAM changes, disk reads and CPU. The server does not report them. Short or overlapping jobs can be merged or split.
- **The model load progress bar** needs one finished load to learn the total size.
- **The boot ETA** needs one measured boot of the same kind, warm or cold. Whether a boot is warm is guessed from whether a volume exists and how long the copy runs.
- **Cancelled generations** are counted from server log lines. Draw Things often restarts a cancelled job straight away, which costs GPU time that `dt` can show but not stop.

## Security trade-offs

- **SSH host keys are not checked.** Servers are new every time, so `ssh` runs with `StrictHostKeyChecking=no`.
- **The shared secret is visible** in the instance's user data and on the container command line on the server.
- **Direct connections are not encrypted.** Using the server's public IP with `A` sends prompts and images in plain text. Use the tunnel where you can.

## Platforms

- **macOS is the target.** The binary builds for Linux and Windows, ARM and x86_64.
- **Notifications are macOS only.** They use `osascript` and silently do nothing elsewhere.
- **The default `local_models_dir` is the macOS Draw Things folder.** Set it by hand on other systems.
- **Windows is untested.** It needs the OpenSSH client, which Windows 10 and 11 include.

## Spot

- **No automatic relaunch after an interruption.** AWS gives 2 minutes notice, shown in red. Press `l` once the old server has gone.

## Display

- **Long log lines wrap** in the Activity panel on the dashboard.
- **The header is cut off** on narrow terminals. The cost details beside the headline are dropped first.
