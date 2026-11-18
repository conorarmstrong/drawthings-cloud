package main

import (
	"archive/zip"
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	et "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebt "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamt "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lt "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// The boot script runs on the server (Linux), so it stays a shell script, embedded in the binary.
//
//go:embed assets/userdata.sh
var userdataTmpl string

// The volume reaper Lambda, deployed to every region a server runs in.
//
//go:embed assets/reaper.py
var reaperPy []byte

// launchRow is one line of the live launch table: an instance type in a region.
type launchRow struct {
	Type, Region string
	Price        float64 // USD per hour, 0 when unknown
	Status       string
	Detail       string
}

type launchRowMsg launchRow

var errCancelled = errors.New("launch cancelled")

// job is one long operation (launch, terminate, ...). out streams progress lines to the log.
type job struct {
	c    Config
	root string
	ctx  context.Context
	out  func(string)
	row  func(launchRow)
	dry  bool // launch: ask EC2 to validate run-instances without starting anything

	iamOnce sync.Once
	iamErr  error
	keyMu   sync.Mutex
}

func (j *job) logf(region, f string, a ...any) {
	tag := ""
	if region != "" {
		tag = "[" + region + "] "
	}
	j.out(tag + fmt.Sprintf(f, a...))
}

func (j *job) setRow(r launchRow) {
	if j.row != nil {
		j.row(r)
	}
}

func (j *job) state(name string) string { return filepath.Join(j.root, ".state", name) }

func secs(t time.Time) string { return fmt.Sprintf("%.0fs", time.Since(t).Seconds()) }

func market(spot bool) string { return onoff(spot, "spot", "on-demand") }

func uniq(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// capacityCodes are launch errors that mean "try somewhere else", not "something is broken".
var capacityCodes = []string{"InsufficientInstanceCapacity", "MaxSpotInstanceCountExceeded", "VcpuLimitExceeded",
	"SpotMaxPriceTooLow", "Unsupported", "UnsupportedOperation", "InsufficientCapacity"}

func reason(err error) string {
	code := errCode(err)
	for _, c := range capacityCodes {
		if code == c {
			return code
		}
	}
	if code != "" {
		return code
	}
	return truncate(err.Error(), 120)
}

// launch starts the server: for each instance type (preferred first, then fallbacks) it tries every
// region that offers the type, cheapest first. If all fail it waits and starts over until
// launch_retry_minutes runs out. Cancelling ctx stops it at the next step.
func (j *job) launch(relaunch bool) (string, error) {
	c, t0 := j.c, time.Now()
	if relaunch {
		if out, err := j.freeVolumes(); err != nil {
			j.logf("", "force-detach failed, the new server may not get the volume: %v", err)
		} else {
			j.logf("", "%s", out)
		}
	}
	types := uniq(append([]string{c.InstanceType}, c.FallbackTypes...))
	j.logf("", "start: types=%v market=%s regions=%v bucket=%s (%s) retry window %dm%s", types, market(c.Spot),
		c.Regions, c.Bucket, c.BucketRegion, c.RetryMin, onoff(j.dry, " DRY RUN", ""))

	running, errs := eachRegion(c.Regions, func(r string) ([]et.Instance, error) { return projectInstances(c, r, "pending", "running") })
	for i, r := range c.Regions {
		if errs[i] != nil {
			j.logf(r, "could not check for a running server: %v", errs[i])
		} else if len(running[i]) > 0 {
			return "", fmt.Errorf("refusing: server already running in %s: %s", r, aws.ToString(running[i][0].InstanceId))
		}
	}
	j.logf("", "no running server in any region (checked in %s)", secs(t0))

	wait := 30 * time.Second
	for round := 1; ; round++ {
		j.logf("", "==== round %d", round)
		var fails []string
		for _, ty := range types {
			if j.ctx.Err() != nil {
				return "", errCancelled
			}
			id, why, err := j.tryType(ty)
			if err == nil {
				j.logf("", "%s after %s in total", onoff(j.dry, "dry run passed", "launched "+id), secs(t0))
				return id, nil
			}
			if errors.Is(err, errCancelled) {
				return "", err
			}
			fails = append(fails, ty+": "+why)
		}
		left := time.Duration(c.RetryMin)*time.Minute - time.Since(t0)
		if left <= wait {
			return "", fmt.Errorf("launch failed in all regions after %s and %d round(s): %s", secs(t0), round, strings.Join(fails, "; "))
		}
		j.logf("", "nothing available this round (%s). Retrying in %s, %s of the retry window left. Press c to stop.",
			strings.Join(fails, "; "), wait, left.Round(time.Second))
		select {
		case <-j.ctx.Done():
			return "", errCancelled
		case <-time.After(wait):
		}
		wait = min(wait*2, 240*time.Second)
	}
}

// priceIn is the current hourly price of typ in region for the configured market.
func (j *job) priceIn(region, typ string) (float64, error) {
	if j.c.Spot {
		p, err := fetchSpotIn(region, []string{typ})
		return p[typ], err
	}
	return fetchPriceIn(j.c, region, typ)
}

// orderByPrice sorts regions cheapest first. Unknown prices (0) go last, in config order.
func orderByPrice(regions []string, prices []float64) []string {
	idx := make([]int, len(regions))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		pa, pb := prices[idx[a]], prices[idx[b]]
		if pa == 0 || pb == 0 {
			return pa != 0 && pb == 0
		}
		return pa < pb
	})
	out := make([]string, len(idx))
	for i, k := range idx {
		out[i] = regions[k]
	}
	return out
}

// tryType tries one instance type across all regions, cheapest first.
func (j *job) tryType(ty string) (id, why string, err error) {
	c := j.c
	j.logf("", "---- instance type %s", ty)
	offers, errs := eachRegion(c.Regions, func(r string) ([]string, error) { return offeredTypes(r, []string{ty}) })
	var ok []string
	for i, r := range c.Regions {
		switch {
		case errs[i] != nil:
			j.logf(r, "skipping: offering lookup failed: %v", errs[i])
			j.setRow(launchRow{Type: ty, Region: r, Status: "error", Detail: errs[i].Error()})
		case len(offers[i]) == 0:
			j.logf(r, "skipping: does not offer %s", ty)
			j.setRow(launchRow{Type: ty, Region: r, Status: "not offered"})
		default:
			ok = append(ok, r)
		}
	}
	if len(ok) == 0 {
		return "", "not offered in any configured region", errors.New("not offered")
	}
	prices, perrs := eachRegion(ok, func(r string) (float64, error) { return j.priceIn(r, ty) })
	pmap := map[string]float64{}
	var parts []string
	for i, r := range ok {
		pmap[r] = prices[i]
		if perrs[i] != nil {
			j.logf(r, "price lookup failed: %v", perrs[i])
		}
	}
	order := orderByPrice(ok, prices)
	for _, r := range order {
		parts = append(parts, fmt.Sprintf("%s=%s", r, priceStr(pmap[r])))
		j.setRow(launchRow{Type: ty, Region: r, Price: pmap[r], Status: "queued"})
	}
	j.logf("", "price order %s (USD/h, %s): %s", ty, market(c.Spot), strings.Join(parts, " "))

	_, serrs := eachRegion(order, func(r string) (struct{}, error) { return struct{}{}, j.ensureRegion(r) })
	var reasons []string
	for i, r := range order {
		if j.ctx.Err() != nil {
			return "", "", errCancelled
		}
		if serrs[i] != nil {
			j.setRow(launchRow{Type: ty, Region: r, Price: pmap[r], Status: "setup failed", Detail: serrs[i].Error()})
			reasons = append(reasons, r+": setup failed")
			continue
		}
		j.setRow(launchRow{Type: ty, Region: r, Price: pmap[r], Status: "trying"})
		j.logf(r, "== trying %s", ty)
		t := time.Now()
		id, zone, err := j.launchIn(r, ty)
		if err == nil {
			j.setRow(launchRow{Type: ty, Region: r, Price: pmap[r], Status: "launched", Detail: zone})
			j.logf(r, "%s %s in %s after %s", onoff(j.dry, "would launch", "launched"), ty, zone, secs(t))
			if c.ModelVolume.Enabled && !j.dry {
				j.attachVolume(r, id, zone)
			}
			return id, "", nil
		}
		if errors.Is(err, errCancelled) {
			j.setRow(launchRow{Type: ty, Region: r, Price: pmap[r], Status: "cancelled"})
			return "", "", err
		}
		why := reason(err)
		j.setRow(launchRow{Type: ty, Region: r, Price: pmap[r], Status: onoff(why == "InsufficientInstanceCapacity", "no capacity", "failed"), Detail: why})
		j.logf(r, "failed after %s (%s), moving on", secs(t), why)
		reasons = append(reasons, r+": "+why)
	}
	return "", strings.Join(reasons, ", "), errors.New("no capacity")
}

func priceStr(p float64) string {
	if p <= 0 {
		return "?"
	}
	return fmt.Sprintf("%.4f", p)
}

// allowedCIDRs resolves allowed_cidrs, where "auto" is this Mac's public IP.
func allowedCIDRs(c Config) ([]string, error) {
	var out []string
	for _, s := range c.AllowedCIDRs {
		if s == "auto" {
			if s = publicIP(); s == "" {
				return nil, fmt.Errorf("could not work out your public IP for allowed_cidrs \"auto\"")
			}
		}
		if !strings.Contains(s, "/") {
			s += "/32"
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("allowed_cidrs is empty in config.json")
	}
	return out, nil
}

// syncRules makes SSH open to exactly cidrs and closes the server port (the tunnel is used instead).
func (j *job) syncRules(region, sg string, cidrs []string) error {
	old, err := rulesOf(region, sg)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, c := range cidrs {
		want[c] = true
		if c == "0.0.0.0/0" {
			j.logf(region, "WARNING: SSH open to the whole internet (key login only)")
		}
	}
	have := map[string]bool{}
	for _, r := range old {
		switch {
		case r.Port == 22 && want[r.CIDR]:
			have[r.CIDR] = true
		case r.Port == 22 || r.Port == j.c.ServerPort:
			if err := setRule(region, sg, false, r.CIDR, r.Port); err != nil {
				return fmt.Errorf("revoke %s:%d: %s", r.CIDR, r.Port, apiErr(err))
			}
			j.logf(region, "removed rule %s port %d", r.CIDR, r.Port)
		}
	}
	for _, c := range cidrs {
		if !have[c] {
			if err := setRule(region, sg, true, c, 22); err != nil && errCode(err) != "InvalidPermission.Duplicate" {
				return fmt.Errorf("allow %s: %s", c, apiErr(err))
			}
		}
	}
	return nil
}

func defaultVPC(ctx context.Context, region string) (string, error) {
	out, err := ec2c(region).DescribeVpcs(ctx, &ec2.DescribeVpcsInput{Filters: []et.Filter{filt("isDefault", "true")}})
	if err != nil {
		return "", fmt.Errorf("%s", apiErr(err))
	}
	if len(out.Vpcs) == 0 {
		return "", nil
	}
	return aws.ToString(out.Vpcs[0].VpcId), nil
}

type subnet struct{ zone, id string }

// renderUserdata fills in the boot script's @@PLACEHOLDERS@@.
func renderUserdata(c Config, volume string) string {
	secret := onelse(c.SharedSecret, "-")
	b := func(v bool) string { return onoff(v, "1", "0") }
	return strings.NewReplacer(
		"@@BUCKET@@", c.Bucket, "@@BUCKET_REGION@@", c.BucketRegion, "@@IDLE_MIN@@", strconv.Itoa(c.IdleMin),
		"@@MAX_HOURS@@", strconv.Itoa(c.MaxHours), "@@CPU_OFFLOAD@@", b(c.CPUOffload), "@@PORT@@", strconv.Itoa(c.ServerPort),
		"@@IMAGE@@", c.DockerImage, "@@GPU_IDLE@@", strconv.Itoa(c.GPUIdlePct), "@@BOOT_TIMEOUT@@", strconv.Itoa(c.BootTimeoutMin),
		"@@IMGCACHE@@", b(c.ImageCache), "@@VOLUME_ID@@", volume, "@@SECRET@@", secret,
	).Replace(userdataTmpl)
}

var hintRE = regexp.MustCompile(`choosing ([a-z0-9-]*[a-z])`)

// zoneHint is the zone AWS suggests in a capacity error ("...or choosing eu-west-2a").
func zoneHint(msg string) string {
	if m := hintRE.FindStringSubmatch(msg); m != nil {
		return m[1]
	}
	return ""
}

// launchIn launches in one region, trying its zones in order. Returns the instance id and zone.
func (j *job) launchIn(region, ty string) (string, string, error) {
	c, ctx := j.c, j.ctx
	b, err := os.ReadFile(j.state("sg_id." + region))
	sg := strings.TrimSpace(string(b))
	if err != nil || sg == "" {
		return "", "", fmt.Errorf("no security group recorded for %s", region)
	}
	if ins, err := projectInstances(c, region, "pending", "running"); err != nil {
		return "", "", err
	} else if len(ins) > 0 {
		return "", "", fmt.Errorf("already running: %s", aws.ToString(ins[0].InstanceId))
	}

	cidrs, err := allowedCIDRs(c)
	if err != nil {
		return "", "", err
	}
	j.logf(region, "security group %s, SSH from %s", sg, strings.Join(cidrs, " "))
	if j.dry {
		j.logf(region, "dry run: leaving the security group rules unchanged")
	} else if err := j.syncRules(region, sg, cidrs); err != nil {
		return "", "", err
	}

	p, err := ssmc(region).GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(c.AMIParam)})
	if err != nil {
		return "", "", fmt.Errorf("AMI lookup: %s", apiErr(err))
	}
	ami := aws.ToString(p.Parameter.Value)
	j.logf(region, "ami %s", ami)

	vpc, err := defaultVPC(ctx, region)
	if err != nil || vpc == "" {
		return "", "", fmt.Errorf("no default VPC in %s %v", region, err)
	}
	so, err := ec2c(region).DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: []et.Filter{filt("vpc-id", vpc), filt("default-for-az", "true")}})
	if err != nil {
		return "", "", fmt.Errorf("subnets: %s", apiErr(err))
	}
	oo, err := ec2c(region).DescribeInstanceTypeOfferings(ctx, &ec2.DescribeInstanceTypeOfferingsInput{
		LocationType: et.LocationTypeAvailabilityZone, Filters: []et.Filter{filt("instance-type", ty)}})
	if err != nil {
		return "", "", fmt.Errorf("zone offerings: %s", apiErr(err))
	}
	offered := map[string]bool{}
	for _, o := range oo.InstanceTypeOfferings {
		offered[aws.ToString(o.Location)] = true
	}
	var subs []subnet
	for _, s := range so.Subnets {
		if z := aws.ToString(s.AvailabilityZone); offered[z] {
			subs = append(subs, subnet{z, aws.ToString(s.SubnetId)})
		}
	}
	sort.Slice(subs, func(a, b int) bool { return subs[a].zone < subs[b].zone })
	if len(subs) == 0 {
		return "", "", fmt.Errorf("no zone in %s offers %s", region, ty)
	}
	var zs []string
	for _, s := range subs {
		zs = append(zs, s.zone)
	}
	j.logf(region, "zones offering %s: %s", ty, strings.Join(zs, " "))

	// Zones that already hold a model volume go first, so the server boots warm.
	if c.ModelVolume.Enabled {
		vols, _ := describeVolumesIn(c, region, filt("tag:Name", c.ModelVolume.Name))
		has := map[string]bool{}
		for _, v := range vols {
			has[v.AZ] = true
		}
		sort.SliceStable(subs, func(a, b int) bool { return has[subs[a].zone] && !has[subs[b].zone] })
		zs = zs[:0]
		for _, s := range subs {
			zs = append(zs, s.zone+onoff(has[s.zone], "(volume)", ""))
		}
		j.logf(region, "zone order, zones holding a model volume first: %s", strings.Join(zs, " "))
	}
	if !j.dry {
		if err := j.ensureReaper(region, false); err != nil {
			j.logf(region, "reaper update skipped: %v", err)
		}
	}

	ud := renderUserdata(c, onoff(c.ModelVolume.Enabled, "auto", "none"))
	_ = os.WriteFile(j.state("userdata.sh"), []byte(ud), 0o600)
	in := &ec2.RunInstancesInput{
		InstanceType: et.InstanceType(ty), ImageId: aws.String(ami), KeyName: aws.String(c.KeyName),
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1),
		IamInstanceProfile:                &et.IamInstanceProfileSpecification{Name: aws.String(c.RoleName)},
		SecurityGroupIds:                  []string{sg},
		InstanceInitiatedShutdownBehavior: et.ShutdownBehaviorTerminate,
		MetadataOptions:                   &et.InstanceMetadataOptionsRequest{HttpTokens: et.HttpTokensStateRequired, HttpPutResponseHopLimit: aws.Int32(2)},
		BlockDeviceMappings: []et.BlockDeviceMapping{{DeviceName: aws.String("/dev/sda1"), Ebs: &et.EbsBlockDevice{
			VolumeSize: aws.Int32(int32(c.VolumeGB)), VolumeType: et.VolumeTypeGp3, Iops: aws.Int32(16000), Throughput: aws.Int32(1000),
			DeleteOnTermination: aws.Bool(true)}}},
		TagSpecifications: []et.TagSpecification{{ResourceType: et.ResourceTypeInstance, Tags: tags("Project", c.ProjectTag, "Name", c.ProjectTag+"-server")}},
		UserData:          aws.String(base64.StdEncoding.EncodeToString([]byte(ud))),
		DryRun:            aws.Bool(j.dry),
	}
	if c.Spot {
		in.InstanceMarketOptions = &et.InstanceMarketOptionsRequest{MarketType: et.MarketTypeSpot, SpotOptions: &et.SpotMarketOptions{
			SpotInstanceType: et.SpotInstanceTypeOneTime, InstanceInterruptionBehavior: et.InstanceInterruptionBehaviorTerminate}}
	}
	j.logf(region, "%s, root disk %dGB, model volume %v", market(c.Spot), c.VolumeGB, c.ModelVolume.Enabled)

	// Zones are tried in order. When AWS names a zone that has capacity, that one jumps the queue.
	pending, tried := append([]subnet{}, subs...), map[string]bool{}
	var last error
	for len(pending) > 0 {
		if ctx.Err() != nil {
			return "", "", errCancelled
		}
		s := pending[0]
		pending = pending[1:]
		if tried[s.zone] {
			continue
		}
		tried[s.zone] = true
		in.SubnetId = aws.String(s.id)
		j.logf(region, "run-instances %s in %s (%s)", ty, s.zone, s.id)
		out, err := ec2c(region).RunInstances(ctx, in, func(o *ec2.Options) { o.RetryMaxAttempts = 1 })
		if err != nil && ctx.Err() != nil {
			return "", "", errCancelled
		}
		if j.dry && errCode(err) == "DryRunOperation" {
			j.logf(region, "dry run: AWS would accept this launch in %s. Nothing was started.", s.zone)
			return "dry-run", s.zone, nil
		}
		if err == nil {
			id := aws.ToString(out.Instances[0].InstanceId)
			j.logf(region, "launched %s in %s", id, s.zone)
			_ = os.WriteFile(j.state("last_region"), []byte(region+"\n"), 0o644)
			return id, s.zone, nil
		}
		last = err
		j.logf(region, "run-instances failed in %s: %s", s.zone, truncate(apiErr(err), 300))
		if h := zoneHint(apiErr(err)); h != "" && !tried[h] {
			for _, e := range subs {
				if e.zone == h {
					j.logf(region, "AWS suggests %s, trying it next", h)
					pending = append([]subnet{e}, pending...)
				}
			}
		}
	}
	j.logf(region, "launch failed in all zones")
	return "", "", last
}

// attachVolume finds or creates the zone's model volume and attaches it. It runs after a successful
// launch, so it ignores the launch cancel: the server is up either way and would sync to its root disk.
func (j *job) attachVolume(region, id, zone string) {
	c := j.c
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cl := ec2c(region)
	mv := c.ModelVolume
	var vol string
	if vs, _ := describeVolumesIn(c, region, filt("tag:Name", mv.Name), filt("availability-zone", zone)); len(vs) > 0 {
		vol = vs[0].ID
		j.logf(region, "found model volume %s in %s, applying iops=%d throughput=%d", vol, zone, mv.IOPS, mv.ThroughputMiBs)
		if _, err := cl.ModifyVolume(ctx, &ec2.ModifyVolumeInput{VolumeId: aws.String(vol), Iops: aws.Int32(int32(mv.IOPS)),
			Throughput: aws.Int32(int32(mv.ThroughputMiBs))}); err != nil {
			j.logf(region, "modify-volume skipped: %s", truncate(apiErr(err), 160))
		}
	} else {
		j.logf(region, "no model volume in %s, creating %dGB gp3 (%d iops, %d MiB/s). The first boot syncs models onto it.", zone, mv.SizeGB, mv.IOPS, mv.ThroughputMiBs)
		out, err := cl.CreateVolume(ctx, &ec2.CreateVolumeInput{AvailabilityZone: aws.String(zone), Size: aws.Int32(int32(mv.SizeGB)),
			VolumeType: et.VolumeTypeGp3, Iops: aws.Int32(int32(mv.IOPS)), Throughput: aws.Int32(int32(mv.ThroughputMiBs)),
			TagSpecifications: []et.TagSpecification{{ResourceType: et.ResourceTypeVolume, Tags: tags("Name", mv.Name, "Project", c.ProjectTag)}}})
		if err != nil {
			j.logf(region, "WARNING: could not create the model volume (%s), the server will use its root disk", truncate(apiErr(err), 200))
			return
		}
		vol = aws.ToString(out.VolumeId)
		if err := ec2.NewVolumeAvailableWaiter(cl).Wait(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{vol}}, 5*time.Minute); err != nil {
			j.logf(region, "volume %s not available yet: %v", vol, err)
		}
		j.logf(region, "created %s", vol)
	}
	j.logf(region, "waiting for %s to be running", id)
	if err := ec2.NewInstanceRunningWaiter(cl).Wait(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}}, 10*time.Minute); err != nil {
		j.logf(region, "instance not running yet: %v", err)
	}
	// the volume may still be held by a server that is shutting down: retry for up to 10 minutes
	for try := 1; try <= 60; try++ {
		_, err := cl.AttachVolume(ctx, &ec2.AttachVolumeInput{VolumeId: aws.String(vol), InstanceId: aws.String(id), Device: aws.String("/dev/sdf")})
		if err == nil {
			j.logf(region, "attached %s to %s (attempt %d)", vol, id, try)
			return
		}
		if try == 1 {
			j.logf(region, "waiting for the volume to be released: %s", truncate(apiErr(err), 120))
		}
		select {
		case <-ctx.Done():
			try = 60
		case <-time.After(10 * time.Second):
		}
	}
	j.logf(region, "WARNING: attach failed, the server will sync to its root disk")
}

// ---- one-time region setup: IAM role, default VPC, security group, key pair, reaper ----

func (j *job) ensureRegion(region string) error {
	if _, err := os.Stat(j.state("sg_id." + region)); err == nil {
		return nil
	}
	if j.dry {
		return fmt.Errorf("not set up yet, a dry run does not create anything")
	}
	j.logf(region, "setting up (IAM role, default VPC, security group, key pair, reaper) ...")
	t := time.Now()
	if err := j.setupRegion(region); err != nil {
		j.logf(region, "setup FAILED after %s: %v", secs(t), err)
		return err
	}
	j.logf(region, "setup ok in %s", secs(t))
	return nil
}

func (j *job) setupRegion(region string) error {
	c, ctx := j.c, j.ctx
	j.iamOnce.Do(func() { j.iamErr = j.ensureIAM() })
	if j.iamErr != nil {
		return j.iamErr
	}
	cl := ec2c(region)
	vpc, err := defaultVPC(ctx, region)
	if err != nil {
		return err
	}
	if vpc == "" {
		j.logf(region, "no default VPC, creating one (free, the standard AWS default network)")
		if _, err := cl.CreateDefaultVpc(ctx, &ec2.CreateDefaultVpcInput{}); err != nil {
			return fmt.Errorf("create default VPC: %s", apiErr(err))
		}
		if vpc, err = defaultVPC(ctx, region); err != nil || vpc == "" {
			return fmt.Errorf("default VPC not visible after creating it %v", err)
		}
		j.logf(region, "default VPC %s created", vpc)
	}
	sgs, err := cl.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{Filters: []et.Filter{filt("group-name", c.SGName), filt("vpc-id", vpc)}})
	if err != nil {
		return fmt.Errorf("security groups: %s", apiErr(err))
	}
	var sg string
	if len(sgs.SecurityGroups) > 0 {
		sg = aws.ToString(sgs.SecurityGroups[0].GroupId)
	} else {
		out, err := cl.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{GroupName: aws.String(c.SGName),
			Description: aws.String("Draw Things SSH from my IP"), VpcId: aws.String(vpc)})
		if err != nil {
			return fmt.Errorf("create security group: %s", apiErr(err))
		}
		sg = aws.ToString(out.GroupId)
	}
	j.logf(region, "security group %s in %s", sg, vpc)

	pub, err := j.localKey()
	if err != nil {
		return err
	}
	if _, err := cl.DescribeKeyPairs(ctx, &ec2.DescribeKeyPairsInput{KeyNames: []string{c.KeyName}}); err != nil {
		if _, err := cl.ImportKeyPair(ctx, &ec2.ImportKeyPairInput{KeyName: aws.String(c.KeyName), PublicKeyMaterial: pub}); err != nil {
			return fmt.Errorf("import key pair: %s", apiErr(err))
		}
		j.logf(region, "imported key pair %s", c.KeyName)
	}
	if err := j.ensureReaper(region, true); err != nil {
		return fmt.Errorf("reaper: %v", err)
	}
	return os.WriteFile(j.state("sg_id."+region), []byte(sg+"\n"), 0o644)
}

// localKey creates the SSH key on this Mac if needed and returns its public half.
func (j *job) localKey() ([]byte, error) {
	j.keyMu.Lock()
	defer j.keyMu.Unlock()
	kf := expandHome(j.c.KeyFile)
	if _, err := os.Stat(kf); err != nil {
		j.logf("", "creating SSH key %s", kf)
		if out, err := sh(30*time.Second, "ssh-keygen", "-t", "ed25519", "-N", "", "-C", "drawthings-aws", "-f", kf); err != nil {
			return nil, fmt.Errorf("ssh-keygen: %s", out)
		}
	}
	return os.ReadFile(kf + ".pub")
}

func noSuchEntity(err error) bool {
	var e *iamt.NoSuchEntityException
	return errors.As(err, &e)
}

func (j *job) ensureIAM() error {
	c, ctx, cl := j.c, j.ctx, iamc()
	j.logf("", "IAM role %s", c.RoleName)
	if _, err := cl.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(c.RoleName)}); err != nil {
		if !noSuchEntity(err) {
			return fmt.Errorf("get role: %s", apiErr(err))
		}
		if _, err := cl.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(c.RoleName), AssumeRolePolicyDocument: aws.String(
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)}); err != nil {
			return fmt.Errorf("create role: %s", apiErr(err))
		}
	}
	pol := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
 {"Effect":"Allow","Action":"s3:ListBucket","Resource":"arn:aws:s3:::%[1]s","Condition":{"StringLike":{"s3:prefix":["models/*","models"]}}},
 {"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::%[1]s/models/*"},
 {"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject"],"Resource":"arn:aws:s3:::%[1]s/image/*"}]}`, c.Bucket)
	if _, err := cl.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String(c.RoleName), PolicyName: aws.String("models-read"), PolicyDocument: aws.String(pol)}); err != nil {
		return fmt.Errorf("role policy: %s", apiErr(err))
	}
	if _, err := cl.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{InstanceProfileName: aws.String(c.RoleName)}); err != nil {
		if !noSuchEntity(err) {
			return fmt.Errorf("get instance profile: %s", apiErr(err))
		}
		if _, err := cl.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{InstanceProfileName: aws.String(c.RoleName)}); err != nil {
			return fmt.Errorf("create instance profile: %s", apiErr(err))
		}
		if _, err := cl.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{InstanceProfileName: aws.String(c.RoleName), RoleName: aws.String(c.RoleName)}); err != nil {
			return fmt.Errorf("add role to profile: %s", apiErr(err))
		}
		time.Sleep(10 * time.Second) // a new instance profile takes a moment before EC2 accepts it
	}
	return nil
}

func reaperZip() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("handler.py")
	if err == nil {
		_, err = w.Write(reaperPy)
	}
	if err == nil {
		err = zw.Close()
	}
	return buf.Bytes(), err
}

// ensureReaper deploys the Lambda that deletes the model volume after it sits unattached.
// Without full it only refreshes the settings of an existing function (done at every launch).
func (j *job) ensureReaper(region string, full bool) error {
	c, ctx := j.c, j.ctx
	fn, rrole := c.ProjectTag+"-volume-reaper", c.RoleName+"-reaper"
	env := &lt.Environment{Variables: map[string]string{"PROJECT_TAG": c.ProjectTag, "VOLUME_NAME": c.ModelVolume.Name,
		"DELETE_AFTER_MIN": strconv.Itoa(c.ModelVolume.DeleteAfterMin)}}
	lc := lambdac(region)
	_, gerr := lc.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fn)})
	exists := gerr == nil
	wait := func() {
		_ = lambda.NewFunctionUpdatedV2Waiter(lc).Wait(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fn)}, time.Minute)
	}
	if exists {
		wait()
		if _, err := lc.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{FunctionName: aws.String(fn), Environment: env}); err != nil {
			return fmt.Errorf("update settings: %s", apiErr(err))
		}
		if !full {
			j.logf(region, "reaper settings updated (delete after %d min)", c.ModelVolume.DeleteAfterMin)
			return nil
		}
	}
	id, err := stsc().GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("account id: %s", apiErr(err))
	}
	acct := aws.ToString(id.Account)
	ic := iamc()
	newRole := false
	if _, err := ic.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(rrole)}); err != nil {
		if !noSuchEntity(err) {
			return fmt.Errorf("get reaper role: %s", apiErr(err))
		}
		if _, err := ic.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(rrole), AssumeRolePolicyDocument: aws.String(
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`)}); err != nil {
			return fmt.Errorf("create reaper role: %s", apiErr(err))
		}
		newRole = true
	}
	pol := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
 {"Effect":"Allow","Action":["logs:CreateLogGroup","logs:CreateLogStream","logs:PutLogEvents"],"Resource":"arn:aws:logs:*:*:*"},
 {"Effect":"Allow","Action":"ec2:DescribeVolumes","Resource":"*"},
 {"Effect":"Allow","Action":["ec2:CreateTags","ec2:DeleteTags","ec2:DeleteVolume"],"Resource":"arn:aws:ec2:*:%s:volume/*","Condition":{"StringEquals":{"aws:ResourceTag/Project":"%s"}}}]}`, acct, c.ProjectTag)
	if _, err := ic.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String(rrole), PolicyName: aws.String("reaper"), PolicyDocument: aws.String(pol)}); err != nil {
		return fmt.Errorf("reaper policy: %s", apiErr(err))
	}
	if newRole {
		time.Sleep(12 * time.Second) // a new role takes a moment before Lambda accepts it
	}
	zb, err := reaperZip()
	if err != nil {
		return err
	}
	if exists {
		wait()
		if _, err := lc.UpdateFunctionCode(ctx, &lambda.UpdateFunctionCodeInput{FunctionName: aws.String(fn), ZipFile: zb}); err != nil {
			return fmt.Errorf("update code: %s", apiErr(err))
		}
	} else if _, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{FunctionName: aws.String(fn), Runtime: lt.RuntimePython312,
		Handler: aws.String("handler.handler"), Role: aws.String("arn:aws:iam::" + acct + ":role/" + rrole), Code: &lt.FunctionCode{ZipFile: zb},
		Timeout: aws.Int32(30), MemorySize: aws.Int32(128), Environment: env}); err != nil {
		return fmt.Errorf("create function: %s", apiErr(err))
	}
	wait()
	rule := fn + "-schedule"
	if _, err := ebc(region).PutRule(ctx, &eventbridge.PutRuleInput{Name: aws.String(rule), ScheduleExpression: aws.String("rate(5 minutes)")}); err != nil {
		return fmt.Errorf("schedule: %s", apiErr(err))
	}
	_, _ = lc.AddPermission(ctx, &lambda.AddPermissionInput{FunctionName: aws.String(fn), StatementId: aws.String(rule), Action: aws.String("lambda:InvokeFunction"),
		Principal: aws.String("events.amazonaws.com"), SourceArn: aws.String("arn:aws:events:" + region + ":" + acct + ":rule/" + rule)})
	if _, err := ebc(region).PutTargets(ctx, &eventbridge.PutTargetsInput{Rule: aws.String(rule), Targets: []ebt.Target{{Id: aws.String("1"),
		Arn: aws.String("arn:aws:lambda:" + region + ":" + acct + ":function:" + fn)}}}); err != nil {
		return fmt.Errorf("schedule target: %s", apiErr(err))
	}
	j.logf(region, "reaper ok (delete after %d min)", c.ModelVolume.DeleteAfterMin)
	return nil
}
