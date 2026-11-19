#!/bin/bash
# Runs as root at first boot. Placeholders are filled in by renderUserdata in launch.go.
exec > >(tee -a /var/log/drawthings-boot.log) 2>&1
set -x
S=/run/dt; mkdir -p $S
echo @@IDLE_MIN@@ > $S/idle_limit_min
echo @@MAX_HOURS@@ > $S/max_hours
date +%s > $S/boot_ts
EV=/var/log/dt-events.log
# One line per event: "<epoch> <text>". The TUI streams this file live and logs every line.
ev() { echo "$(date +%s) $*" >> $EV; }
export EV
step() { echo "$*" > $S/step; echo "STEP $(date +%T) $*"; ev "STEP $*"; }
imds() { curl -s -m 2 -H "X-aws-ec2-metadata-token: $(curl -s -m 2 -X PUT http://169.254.169.254/latest/api/token -H 'X-aws-ec2-metadata-token-ttl-seconds: 60')" "http://169.254.169.254/latest/meta-data/$1"; }
ev "boot: instance $(imds instance-id) type $(imds instance-type) zone $(imds placement/availability-zone)"
step "Instance booted, starting setup"

cat > /usr/local/bin/dt-watchdog.sh <<'WD'
#!/bin/bash
# Shuts the box down (=> terminate) when nothing has happened for too long, or on max lifetime.
# Busy means: GPU compute, GPU memory changing (model loading), or models being read from disk.
S=/run/dt
ev() { echo "$(date +%s) watchdog: $*" >> /var/log/dt-events.log; }
last=$(date +%s)
mile=0
rd() { awk -v d="$(basename "$(findmnt -no SOURCE -T /opt/models)")" '$3==d{print $6}' /proc/diskstats; }
pr=$(rd); pr=${pr:-0}; pv=0
while true; do
  busy=0
  for k in 1 2 3 4 5 6; do
    u=$(nvidia-smi --query-gpu=utilization.gpu --format=csv,noheader,nounits | head -1)
    [ "${u:-0}" -ge @@GPU_IDLE@@ ] && busy=1
    sleep 5
  done
  vm=$(nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits | head -1); vm=${vm:-0}
  d=$((vm-pv)); [ "${d#-}" -gt 200 ] && busy=1; pv=$vm
  cr=$(rd); cr=${cr:-0}; [ $(( (cr-pr)*512/1048576 )) -gt 30 ] && busy=1; pr=$cr
  now=$(date +%s); boot=$(cat $S/boot_ts)
  maxs=$(( $(cat $S/max_hours) * 3600 )); lim=$(( $(cat $S/idle_limit_min) * 60 ))
  if [ $((now-boot)) -gt $maxs ]; then logger "dt-watchdog: max lifetime"; ev "max lifetime reached, shutting down"; shutdown -h now; fi
  if [ ! -f $S/ready_ts ]; then
    [ $((now-boot)) -gt $((@@BOOT_TIMEOUT@@*60)) ] && { logger "dt-watchdog: never became ready"; ev "never became ready in time, shutting down"; shutdown -h now; }
    echo 0 > $S/idle_secs; continue
  fi
  r=$(cat $S/ready_ts); [ "$last" -lt "$r" ] && last=$r
  [ -f $S/keepalive ] && { last=$now; rm -f $S/keepalive; }
  [ "$busy" = 1 ] && last=$now
  echo $((now-last)) > $S/idle_secs
  if [ "$busy" = 1 ]; then [ "$mile" != 0 ] && ev "activity seen, idle timer reset"; mile=0
  else q=$(( (now-last)*4/lim )); if [ "$q" -gt "$mile" ] && [ "$q" -lt 4 ]; then mile=$q; ev "idle for $((now-last))s of ${lim}s allowed ($((q*25))%)"; fi; fi
  [ $((now-last)) -gt $lim ] && { logger "dt-watchdog: idle"; ev "idle limit reached after $((now-last))s, shutting down"; shutdown -h now; }
done
WD
chmod +x /usr/local/bin/dt-watchdog.sh

cat > /usr/local/bin/dt-timeline.sh <<'TL'
#!/bin/bash
# Every 2s: one CSV line in /var/log/dt-timeline.log, and the derived numbers in /run/dt/derived for the TUI.
S=/run/dt; L=/var/log/dt-timeline.log
echo "time,phase,gpu_pct,vram_mib,power_w,srv_cpu_pct,disk_read_mibs,net_tx_mibs,net_rx_mibs,cache_mib,conns" > $L
tck=$(getconf CLK_TCK); ifc=$(ip -o route get 1.1.1.1 | sed 's/.*dev \([^ ]*\).*/\1/')
pn=$(date +%s%N); ppid=0; pt=0; prs=0; ptx=0; prx=0
while true; do
  sleep 2
  n=$(date +%s%N); dtn=$(( (n-pn)/1000000 )); [ "$dtn" -lt 100 ] && dtn=100; pn=$n
  g=$(nvidia-smi --query-gpu=utilization.gpu,memory.used,power.draw --format=csv,noheader,nounits 2>/dev/null | head -1 | tr -d ' ')
  IFS=, read -r gu mu pw <<< "${g:-0,0,0}"; gu=${gu//[^0-9]/}; mu=${mu//[^0-9]/}; pw=${pw%%.*}; pw=${pw//[^0-9]/}
  pid=$(pgrep -n -x gRPCServerCLI); t=0
  [ -n "$pid" ] && t=$(awk '{print $14+$15}' /proc/$pid/stat 2>/dev/null || echo 0)
  [ "$pid" != "$ppid" ] && { ppid=$pid; pt=$t; }
  cpu=$(( (t-pt)*100000/(tck*dtn) )); [ "$cpu" -lt 0 ] && cpu=0; pt=$t
  dev=$(basename "$(findmnt -no SOURCE -T /opt/models)")
  rs=$(awk -v d="$dev" '$3==d{print $6}' /proc/diskstats); rs=${rs:-0}
  dr=$(( (rs-prs)*512*1000/(1048576*dtn) )); [ "$dr" -lt 0 ] && dr=0; prs=$rs
  tx=$(cat /sys/class/net/$ifc/statistics/tx_bytes); rx=$(cat /sys/class/net/$ifc/statistics/rx_bytes)
  ntx=$(( (tx-ptx)*1000/(1048576*dtn) )); nrx=$(( (rx-prx)*1000/(1048576*dtn) )); ptx=$tx; prx=$rx
  [ "$ntx" -lt 0 ] && ntx=0; [ "$nrx" -lt 0 ] && nrx=0
  cache=$(free -m | awk '/Mem:/{print $6}')
  conns=$(ss -Htn state established '( sport = :@@PORT@@ )' 2>/dev/null | wc -l)
  echo "${cpu:-0} ${dr:-0} ${ntx:-0} ${nrx:-0}" > $S/derived.tmp && mv $S/derived.tmp $S/derived
  echo "$(date +%T),$(cat $S/phase 2>/dev/null),${gu:-0},${mu:-0},${pw:-0},${cpu:-0},${dr:-0},${ntx:-0},${nrx:-0},${cache:-0},$conns" >> $L
done
TL
chmod +x /usr/local/bin/dt-timeline.sh

cat > /usr/local/bin/dt-metrics.sh <<'MT'
#!/bin/bash
# One JSON line every 2s for the TUI.
S=/run/dt
while true; do
  g=$(nvidia-smi --query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw --format=csv,noheader,nounits 2>/dev/null | head -1 | tr -d ' ')
  IFS=, read -r gu mu mt tp pw <<< "${g:-0,0,0,0,0}"
  num() { local v=${1//[^0-9.]/}; echo "${v:-0}"; }
  gu=$(num "$gu"); mu=$(num "$mu"); mt=$(num "$mt"); tp=$(num "$tp"); pw=$(num "$pw")
  load=$(cut -d' ' -f1 /proc/loadavg)
  mem=$(free -m | awk '/Mem:/{printf "%d,%d",$3,$2}'); mu2=${mem%,*}; mt2=${mem#*,}
  conns=$(ss -Htn state established '( sport = :@@PORT@@ )' 2>/dev/null | wc -l)
  now=$(date +%s); up=$((now-$(cat $S/boot_ts)))
  read -r cpu dr ntx nrx < $S/derived 2>/dev/null; cpu=${cpu:-0}; dr=${dr:-0}; ntx=${ntx:-0}
  fd=$(find /opt/models -maxdepth 1 -type f ! -name '*.????????' 2>/dev/null | wc -l)
  mb=$(du -sb /opt/models 2>/dev/null | cut -f1)
  disk=$(df -BG --output=used / | tail -1 | tr -dc 0-9)
  phase=$(cat $S/phase 2>/dev/null)
  tok=$(curl -s -m 2 -X PUT http://169.254.169.254/latest/api/token -H 'X-aws-ec2-metadata-token-ttl-seconds: 60')
  sa=$(curl -s -m 2 -H "X-aws-ec2-metadata-token: $tok" -w '%{http_code}' http://169.254.169.254/latest/meta-data/spot/instance-action | tr -d '\000-\037"\\' )
  spot_warn=0; spot_info=""; case "$sa" in *200) spot_warn=1; spot_info=${sa%200};; esac
  evs=$(docker logs --since 3m dt 2>&1 | grep " info " | tr -d '\r\000-\010\013-\037' | tail -6 | cut -c1-230 | sed 's/\\/\\\\/g; s/"/\\"/g' | awk 'BEGIN{ORS=""} {printf "%s\"%s\"", (NR>1?",":""), $0}')
  log=$(echo "$evs" | sed 's/.*,"//; s/^"//; s/"$//' | cut -c1-160)
  printf '{"phase":"%s","gpu":%s,"vram":%s,"vram_total":%s,"temp":%s,"power":%s,"load":%s,"ram":%s,"ram_total":%s,"conns":%s,"up":%s,"idle":%s,"idle_limit":%s,"max":%s,"models_bytes":%s,"disk_gb":%s,"log":"%s","step":"%s","files_done":%s,"files_total":%s,"disk_read":%s,"srv_cpu":%s,"net_tx":%s,"events":[%s],"spot_warn":%s,"spot_info":"%s"}\n' \
    "$phase" "${gu:-0}" "${mu:-0}" "${mt:-0}" "${tp:-0}" "${pw:-0}" "${load:-0}" "$mu2" "$mt2" "$conns" "$up" \
    "$(cat $S/idle_secs 2>/dev/null || echo 0)" "$(( $(cat $S/idle_limit_min)*60 ))" "$(( $(cat $S/max_hours)*3600 ))" "${mb:-0}" "${disk:-0}" "$log" "$(cat $S/step 2>/dev/null | tr -d '"\\')" "${fd:-0}" "$(cat $S/files_total 2>/dev/null || echo 0)" "${dr:-0}" "${cpu:-0}" "${ntx:-0}" "$evs" "$spot_warn" "${spot_info:0:120}"
  sleep 2
done
MT
chmod +x /usr/local/bin/dt-metrics.sh

echo booting > $S/phase
nohup /usr/local/bin/dt-watchdog.sh >/dev/null 2>&1 &
nohup /usr/local/bin/dt-timeline.sh >/dev/null 2>&1 &

mkdir -p /opt/models
VOL="@@VOLUME_ID@@"
if [ "$VOL" != "none" ]; then
  echo attaching > $S/phase
  step "Waiting for the model volume to attach"
  if [ "$VOL" = auto ]; then
    # the launcher attaches the model volume after boot: it is the one EBS disk that is not the root disk
    rootdev=$(lsblk -no pkname "$(findmnt -no SOURCE /)" 2>/dev/null | head -1)
    DEV=""
    for i in $(seq 1 190); do
      for d in /dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_vol*; do
        [ -e "$d" ] || continue; case "$d" in *-part*) continue;; esac
        [ "$(basename "$(readlink -f "$d")")" = "$rootdev" ] || { DEV=$d; break; }
      done
      [ -n "$DEV" ] && break; sleep 5
    done
    VOL=${DEV##*_}
    ev "volume: found $VOL after $((i*5))s at most"
  else
    DEV=/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_${VOL//-/}
    for i in $(seq 1 190); do [ -e "$DEV" ] && break; sleep 5; done
  fi
  if [ -n "$DEV" ] && [ -e "$DEV" ]; then
    blkid "$DEV" >/dev/null 2>&1 || mkfs.ext4 -q -L dtmodels "$DEV"
    step "Mounting the model volume"
    mount -o noatime "$DEV" /opt/models && { echo "model volume mounted"; ev "volume: $VOL mounted at /opt/models ($(df -h /opt/models | awk 'NR==2{print $2" size, "$3" used"}'))"; }
  else
    echo "model volume never appeared, using root disk"; ev "volume: $VOL never appeared, using root disk"
  fi
fi
export AWS_DEFAULT_REGION=@@BUCKET_REGION@@
# Server image: load a copy from S3 when it is current, else pull from Docker Hub.
# Runs in the background while the models sync, since the two do not depend on each other.
fetch_image() {
  IMAGE=@@IMAGE@@; IMGS3=s3://@@BUCKET@@/image/$(echo "$IMAGE" | tr '/:' '__')
  IMGCACHE=@@IMGCACHE@@; UPLOAD_IMG=0; LOADED=0; cur=""; t0=$(date +%s)
  if [ "$IMGCACHE" = 1 ]; then
    command -v zstd >/dev/null || apt-get install -y -qq zstd >/dev/null 2>&1
    repo=${IMAGE%%:*}; tag=${IMAGE##*:}; [ "$tag" = "$IMAGE" ] && tag=latest
    tok=$(curl -fs -m 15 "https://auth.docker.io/token?service=registry.docker.io&scope=repository:$repo:pull" | sed 's/.*"token":"\([^"]*\)".*/\1/')
    cur=$(curl -fsI -m 15 -H "Authorization: Bearer $tok" -H "Accept: application/vnd.docker.distribution.manifest.v2+json,application/vnd.oci.image.manifest.v1+json" \
      "https://registry-1.docker.io/v2/$repo/manifests/$tag" | tr -d '\r' | awk -F': ' 'tolower($1)=="docker-content-digest"{print $2}')
    have=$(aws s3 cp "$IMGS3.digest" - 2>/dev/null)
    echo "image digest: hub=${cur:-unreachable} s3=${have:-none}"
    if [ -n "$have" ] && { [ -z "$cur" ] || [ "$cur" = "$have" ]; }; then
      echo "loading the server image from S3 (up to date)"; ev "image: loading from S3 cache"
      if ( set -o pipefail; aws s3 cp "$IMGS3.tar.zst" - --only-show-errors | zstd -dc | docker load ); then LOADED=1; fi
    fi
    [ "$LOADED" = 1 ] || UPLOAD_IMG=1
  fi
  if [ "$LOADED" != 1 ]; then
    echo "downloading the server image from Docker Hub ($IMAGE)"; ev "image: pulling $IMAGE from Docker Hub"
    docker pull "$IMAGE"
  fi
  echo "image ready in $(( $(date +%s)-t0 ))s (from S3: $LOADED)"; ev "image: ready in $(( $(date +%s)-t0 ))s (hub digest ${cur:-unknown}, from S3 cache: $LOADED)"
  echo "$UPLOAD_IMG" > $S/img_upload; echo "$cur" > $S/img_cur
}
fetch_image > /var/log/dt-image.log 2>&1 &
IMG_PID=$!

echo syncing > $S/phase
step "Copying models from S3"
aws configure set default.s3.max_concurrent_requests 64
aws configure set default.s3.multipart_chunksize 64MB
ev "model sync: listing s3://@@BUCKET@@/models/ (bucket region @@BUCKET_REGION@@)"
aws s3api list-objects-v2 --bucket @@BUCKET@@ --prefix models/ --query 'Contents[].[Key,Size]' --output text > $S/filelist
WANT="@@MODELS@@"
if [ "$WANT" != "*" ]; then
  awk -v want=",$WANT," 'index(want, "," substr($1,8) ",")' $S/filelist > $S/filelist.f && mv $S/filelist.f $S/filelist
fi
grep -c . $S/filelist > $S/files_total
ev "model sync: $(cat $S/files_total) files, $(awk '{t+=$2} END{printf "%.1f", t/1073741824}' $S/filelist) GiB total, 3 at a time"
sz() { if [ "$1" -ge 1048576 ]; then echo "$(( $1/1048576 )) MiB"; else echo "$1 bytes"; fi; }
export -f sz
fetch_model() {
  local key=$1 size=$2 f=${1#models/}
  [ -n "$f" ] || return 0
  if [ -f "/opt/models/$f" ] && [ "$(stat -c %s "/opt/models/$f")" = "$size" ]; then
    echo "$(date +%s) model $f: already on disk ($(sz $size)), skipped" >> $EV; return 0
  fi
  echo "$(date +%s) model $f: download started ($(sz $size))" >> $EV
  local t=$(date +%s)
  until aws s3 cp "s3://@@BUCKET@@/$key" "/opt/models/$f" --only-show-errors; do
    echo "$(date +%s) model $f: error, retrying in 5s" >> $EV; sleep 5
  done
  local d=$(( $(date +%s)-t )); [ "$d" -lt 1 ] && d=1
  echo "$(date +%s) model $f: received $(sz $size) in ${d}s ($((size/1048576/d)) MiB/s)" >> $EV
}
export -f fetch_model
t_sync=$(date +%s)
grep . $S/filelist | xargs -P 3 -L 1 bash -c 'fetch_model "$0" "$1"'
ev "model sync: all files fetched in $(( $(date +%s)-t_sync ))s, verifying"
SYNCARGS=(); if [ "$WANT" != "*" ]; then SYNCARGS=(--exclude "*"); for f in ${WANT//,/ }; do SYNCARGS+=(--include "$f"); done; fi
until aws s3 sync s3://@@BUCKET@@/models/ /opt/models "${SYNCARGS[@]}" --only-show-errors; do ev "model sync: verify pass failed, retrying"; sleep 10; done
ev "model sync: done, $(du -sh /opt/models 2>/dev/null | cut -f1) on disk"

step "Finishing the server image"
if ! wait $IMG_PID; then echo "image fetch failed, retrying in the foreground"; fetch_image; fi
cat /var/log/dt-image.log
UPLOAD_IMG=$(cat $S/img_upload); cur=$(cat $S/img_cur); IMAGE=@@IMAGE@@; IMGS3=s3://@@BUCKET@@/image/$(echo "$IMAGE" | tr '/:' '__')

echo starting > $S/phase
OFFLOAD=""; [ "@@CPU_OFFLOAD@@" = "1" ] && OFFLOAD="--cpu-offload"
SECARG=""; [ "@@SECRET@@" != "-" ] && SECARG="-s @@SECRET@@"

step "Starting the server container and waiting for port @@PORT@@"
docker rm -f dt 2>/dev/null
docker run -d --name dt --restart unless-stopped --gpus all -p @@PORT@@:@@PORT@@ \
  -v /opt/models:/models @@IMAGE@@ \
  gRPCServerCLI /models -a 0.0.0.0 -p @@PORT@@ --no-tls --model-browser --supervised $OFFLOAD $SECARG

ev "container: started $(docker inspect -f '{{.Id}}' dt 2>/dev/null | cut -c1-12), waiting for port @@PORT@@"
nohup bash -c 'first=1; while true; do
  if [ $first = 1 ]; then since=""; first=0; else since="--since 0s"; fi
  docker logs -f $since dt 2>&1 | grep --line-buffered -E " (info|warning|error|critical) " | while IFS= read -r l; do
    echo "$l" >> /var/log/dt-server-events.log
    echo "$(date +%s) server: $(echo "$l" | sed -E "s/^.* (info|warning|error|critical) [^ ]* : //; s/^.* (info|warning|error|critical) //")" >> /var/log/dt-events.log
  done; sleep 2; done' >/dev/null 2>&1 &
nohup bash -c 'prev=""; while true; do
  cur=$(ss -Htn state established "( sport = :@@PORT@@ )" 2>/dev/null | awk "{print \$4}" | sort -u)
  for c in $(comm -13 <(echo "$prev") <(echo "$cur")); do echo "$(date +%s) connection: opened from $c" >> /var/log/dt-events.log; done
  for c in $(comm -23 <(echo "$prev") <(echo "$cur")); do echo "$(date +%s) connection: closed from $c" >> /var/log/dt-events.log; done
  prev=$cur; sleep 1; done' >/dev/null 2>&1 &
for i in $(seq 1 60); do ss -Htln 'sport = :@@PORT@@' | grep -q . && break; sleep 2; done
date +%s > $S/ready_ts
ev "ready: port @@PORT@@ is listening after $(( $(date +%s)-$(cat $S/boot_ts) ))s from boot"
step "Ready. Safe to connect"
echo ready > $S/phase

# Save a fresh copy of the image to S3 for the next boot. Digest is written last.
if [ "$UPLOAD_IMG" = 1 ] && [ -n "${cur:-}" ]; then
  (
    set -o pipefail
    aws s3 rm "$IMGS3.digest" --only-show-errors
    if docker save "$IMAGE" | zstd -T0 -3 | aws s3 cp - "$IMGS3.tar.zst" --only-show-errors; then
      echo "$cur" | aws s3 cp - "$IMGS3.digest" --only-show-errors && { echo "image saved to S3"; ev "image: saved a fresh copy to the S3 cache"; }
    else
      echo "image save to S3 failed"
    fi
  ) &
fi
