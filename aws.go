package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	et "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	prt "github.com/aws/aws-sdk-go-v2/service/pricing/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// sh runs a local program (ssh, ssh-keygen). AWS calls go through the SDK, not the aws CLI.
func sh(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err == nil {
		return strings.TrimSpace(out.String()), nil
	}
	return strings.TrimSpace(out.String() + errb.String()), err
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return h + p[1:]
	}
	return p
}

// sshOpts are the common ssh arguments for talking to the server.
func sshOpts(c Config) []string {
	return []string{"-i", expandHome(c.KeyFile), "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR", "-o", "ConnectTimeout=8"}
}

type Instance struct {
	ID     string
	State  string
	IP     string
	Type   string
	Spot   bool
	Launch time.Time
	Region string
	Zone   string
}

// eachRegion runs f for every configured region at once and returns the results in region order.
func eachRegion[T any](regions []string, f func(r string) (T, error)) ([]T, []error) {
	res, errs := make([]T, len(regions)), make([]error, len(regions))
	var wg sync.WaitGroup
	for i, r := range regions {
		wg.Add(1)
		go func() { defer wg.Done(); res[i], errs[i] = f(r) }()
	}
	wg.Wait()
	return res, errs
}

// describeInstance looks for the server in every configured region at once.
func describeInstance(c Config) (*Instance, error) {
	rs, errs := eachRegion(c.Regions, func(r string) (*Instance, error) { return describeInstanceIn(c, r) })
	for _, in := range rs {
		if in != nil && in.State != "shutting-down" {
			return in, nil
		}
	}
	for _, in := range rs {
		if in != nil {
			return in, nil
		}
	}
	for _, e := range errs {
		if e != nil {
			return nil, e
		}
	}
	return nil, nil
}

func projectInstances(c Config, region string, states ...string) ([]et.Instance, error) {
	ctx, cancel := ctxT(25 * time.Second)
	defer cancel()
	out, err := ec2c(region).DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: []et.Filter{
		filt("tag:Project", c.ProjectTag), filt("instance-state-name", states...)}})
	if err != nil {
		return nil, fmt.Errorf("%s: %s", region, apiErr(err))
	}
	var all []et.Instance
	for _, r := range out.Reservations {
		all = append(all, r.Instances...)
	}
	return all, nil
}

func toInstance(x et.Instance, region string) *Instance {
	in := &Instance{ID: aws.ToString(x.InstanceId), IP: aws.ToString(x.PublicIpAddress), Type: string(x.InstanceType),
		Spot: x.InstanceLifecycle == et.InstanceLifecycleTypeSpot, Region: region, Launch: aws.ToTime(x.LaunchTime)}
	if x.State != nil {
		in.State = string(x.State.Name)
	}
	if x.Placement != nil {
		in.Zone = aws.ToString(x.Placement.AvailabilityZone)
	}
	return in
}

func describeInstanceIn(c Config, region string) (*Instance, error) {
	all, err := projectInstances(c, region, "pending", "running", "shutting-down")
	if err != nil || len(all) == 0 {
		return nil, err
	}
	pick := all[0]
	for _, x := range all { // prefer a live server over one that is still shutting down
		if x.State != nil && x.State.Name != et.InstanceStateNameShuttingDown {
			pick = x
			break
		}
	}
	return toInstance(pick, region), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

type TypeInfo struct {
	Type    string
	GPU     string
	VRAMGiB float64
	VCPU    int
	RAMGiB  float64
}

// offeredTypes returns which of types the region sells, keeping the given order.
func offeredTypes(region string, types []string) ([]string, error) {
	ctx, cancel := ctxT(25 * time.Second)
	defer cancel()
	out, err := ec2c(region).DescribeInstanceTypeOfferings(ctx, &ec2.DescribeInstanceTypeOfferingsInput{
		Filters: []et.Filter{filt("instance-type", types...)}})
	if err != nil {
		return nil, fmt.Errorf("%s", apiErr(err))
	}
	have := map[string]bool{}
	for _, o := range out.InstanceTypeOfferings {
		have[string(o.InstanceType)] = true
	}
	var res []string
	for _, t := range types {
		if have[t] {
			res = append(res, t)
		}
	}
	return res, nil
}

func fetchTypes(c Config) ([]TypeInfo, error) {
	offered, err := offeredTypes(c.reg(), c.CandidateTypes)
	if err != nil || len(offered) == 0 {
		return nil, err
	}
	var its []et.InstanceType
	for _, t := range offered {
		its = append(its, et.InstanceType(t))
	}
	ctx, cancel := ctxT(25 * time.Second)
	defer cancel()
	out, err := ec2c(c.reg()).DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{InstanceTypes: its})
	if err != nil {
		return nil, fmt.Errorf("%s", apiErr(err))
	}
	byType := map[string]TypeInfo{}
	for _, t := range out.InstanceTypes {
		ti := TypeInfo{Type: string(t.InstanceType)}
		if g := t.GpuInfo; g != nil {
			if len(g.Gpus) > 0 {
				ti.GPU = aws.ToString(g.Gpus[0].Name)
			}
			ti.VRAMGiB = float64(aws.ToInt32(g.TotalGpuMemoryInMiB)) / 1024
		}
		if t.VCpuInfo != nil {
			ti.VCPU = int(aws.ToInt32(t.VCpuInfo.DefaultVCpus))
		}
		if t.MemoryInfo != nil {
			ti.RAMGiB = float64(aws.ToInt64(t.MemoryInfo.SizeInMiB)) / 1024
		}
		byType[ti.Type] = ti
	}
	var res []TypeInfo
	for _, t := range c.CandidateTypes {
		if ti, ok := byType[t]; ok {
			res = append(res, ti)
		}
	}
	return res, nil
}

func fetchPrice(c Config, typ string) (float64, error) { return fetchPriceIn(c, c.reg(), typ) }

// fetchPriceIn is the on-demand Linux price from the AWS Pricing API.
func fetchPriceIn(c Config, region, typ string) (float64, error) {
	loc, ok := c.PricingLocations[region]
	if !ok {
		return 0, fmt.Errorf("no pricing location for %s (add it to pricing_locations)", region)
	}
	f := func(k, v string) prt.Filter {
		return prt.Filter{Type: prt.FilterTypeTermMatch, Field: aws.String(k), Value: aws.String(v)}
	}
	ctx, cancel := ctxT(30 * time.Second)
	defer cancel()
	out, err := pricingc().GetProducts(ctx, &pricing.GetProductsInput{ServiceCode: aws.String("AmazonEC2"), MaxResults: aws.Int32(1),
		Filters: []prt.Filter{f("instanceType", typ), f("location", loc), f("operatingSystem", "Linux"), f("tenancy", "Shared"),
			f("preInstalledSw", "NA"), f("capacitystatus", "Used")}})
	if err != nil {
		return 0, fmt.Errorf("%s", apiErr(err))
	}
	if len(out.PriceList) == 0 {
		return 0, fmt.Errorf("no price")
	}
	var d struct {
		Terms struct {
			OnDemand map[string]struct {
				PriceDimensions map[string]struct {
					PricePerUnit map[string]string
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(out.PriceList[0]), &d); err != nil {
		return 0, err
	}
	for _, t := range d.Terms.OnDemand {
		for _, p := range t.PriceDimensions {
			return strconv.ParseFloat(p.PricePerUnit["USD"], 64)
		}
	}
	return 0, fmt.Errorf("no price")
}

func fetchSpot(c Config, types []string) map[string]float64 {
	res, _ := fetchSpotIn(c.reg(), types)
	return res
}

// fetchSpotIn is the lowest current spot price of each type over the region's zones.
func fetchSpotIn(region string, types []string) (map[string]float64, error) {
	res := map[string]float64{}
	var its []et.InstanceType
	for _, t := range types {
		its = append(its, et.InstanceType(t))
	}
	ctx, cancel := ctxT(30 * time.Second)
	defer cancel()
	out, err := ec2c(region).DescribeSpotPriceHistory(ctx, &ec2.DescribeSpotPriceHistoryInput{InstanceTypes: its,
		ProductDescriptions: []string{"Linux/UNIX"}, StartTime: aws.Time(time.Now().UTC())})
	if err != nil {
		return res, fmt.Errorf("%s", apiErr(err))
	}
	for _, h := range out.SpotPriceHistory {
		p, _ := strconv.ParseFloat(aws.ToString(h.SpotPrice), 64)
		t := string(h.InstanceType)
		if p > 0 && (res[t] == 0 || p < res[t]) {
			res[t] = p
		}
	}
	return res, nil
}

func listS3Models(c Config) (map[string]int64, error) {
	ctx, cancel := ctxT(40 * time.Second)
	defer cancel()
	m := map[string]int64{}
	p := s3.NewListObjectsV2Paginator(s3c(c.BucketRegion), &s3.ListObjectsV2Input{Bucket: aws.String(c.Bucket), Prefix: aws.String("models/")})
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s", apiErr(err))
		}
		for _, o := range out.Contents {
			m[strings.TrimPrefix(aws.ToString(o.Key), "models/")] = aws.ToInt64(o.Size)
		}
	}
	return m, nil
}

// detectBucketRegion asks S3 which region a bucket lives in.
func detectBucketRegion(bucket string) (string, error) {
	ctx, cancel := ctxT(15 * time.Second)
	defer cancel()
	out, err := s3c("us-east-1").GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: aws.String(bucket)})
	if err != nil {
		return "", fmt.Errorf("%s", apiErr(err))
	}
	switch r := string(out.LocationConstraint); r {
	case "":
		return "us-east-1", nil
	case "EU":
		return "eu-west-1", nil
	default:
		return r, nil
	}
}

type Volume struct {
	ID, AZ, State string
	SizeGB        int
	Region        string
}

func describeVolumes(c Config) ([]Volume, error) {
	vs, errs := eachRegion(c.Regions, func(r string) ([]Volume, error) { return describeVolumesIn(c, r) })
	var all []Volume
	for i := range vs {
		if errs[i] != nil {
			return nil, errs[i]
		}
		all = append(all, vs[i]...)
	}
	return all, nil
}

func describeVolumesIn(c Config, region string, extra ...et.Filter) ([]Volume, error) {
	ctx, cancel := ctxT(25 * time.Second)
	defer cancel()
	out, err := ec2c(region).DescribeVolumes(ctx, &ec2.DescribeVolumesInput{Filters: append([]et.Filter{filt("tag:Project", c.ProjectTag)}, extra...)})
	if err != nil {
		return nil, fmt.Errorf("%s: %s", region, apiErr(err))
	}
	var vs []Volume
	for _, v := range out.Volumes {
		vs = append(vs, Volume{aws.ToString(v.VolumeId), aws.ToString(v.AvailabilityZone), string(v.State), int(aws.ToInt32(v.Size)), region})
	}
	return vs, nil
}

// ---- security group rules (who may connect) ----

type Rule struct {
	CIDR string
	Port int
}

func sgID(c Config) (string, error) { return sgIDIn(c, c.reg()) }

func sgIDIn(c Config, region string) (string, error) {
	ctx, cancel := ctxT(20 * time.Second)
	defer cancel()
	out, err := ec2c(region).DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{Filters: []et.Filter{filt("group-name", c.SGName)}})
	if err != nil || len(out.SecurityGroups) == 0 {
		return "", fmt.Errorf("security group %s not found in %s (launch once to create it)", c.SGName, region)
	}
	return aws.ToString(out.SecurityGroups[0].GroupId), nil
}

func rulesOf(region, sg string) ([]Rule, error) {
	ctx, cancel := ctxT(20 * time.Second)
	defer cancel()
	out, err := ec2c(region).DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{sg}})
	if err != nil {
		return nil, fmt.Errorf("%s", apiErr(err))
	}
	var rules []Rule
	for _, g := range out.SecurityGroups {
		for _, p := range g.IpPermissions {
			for _, r := range p.IpRanges {
				rules = append(rules, Rule{aws.ToString(r.CidrIp), int(aws.ToInt32(p.FromPort))})
			}
		}
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Port != rules[j].Port {
			return rules[i].Port < rules[j].Port
		}
		return rules[i].CIDR < rules[j].CIDR
	})
	return rules, nil
}

func listRules(c Config) ([]Rule, error) {
	id, err := sgID(c)
	if err != nil {
		return nil, err
	}
	return rulesOf(c.reg(), id)
}

func setRule(region, sg string, add bool, cidr string, port int) error {
	ctx, cancel := ctxT(25 * time.Second)
	defer cancel()
	perm := []et.IpPermission{{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(int32(port)), ToPort: aws.Int32(int32(port)),
		IpRanges: []et.IpRange{{CidrIp: aws.String(cidr)}}}}
	var err error
	if add {
		_, err = ec2c(region).AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{GroupId: aws.String(sg), IpPermissions: perm})
	} else {
		_, err = ec2c(region).RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{GroupId: aws.String(sg), IpPermissions: perm})
	}
	return err
}

func changeRule(c Config, add bool, cidr string, port int) (string, error) {
	id, err := sgID(c)
	if err != nil {
		return "", err
	}
	if err := setRule(c.reg(), id, add, cidr, port); err != nil {
		if errCode(err) == "InvalidPermission.Duplicate" {
			return "already allowed", nil
		}
		return apiErr(err), err
	}
	return fmt.Sprintf("%s %s port %d", map[bool]string{true: "allowed", false: "removed"}[add], cidr, port), nil
}

func publicIP() string {
	cl := http.Client{Timeout: 8 * time.Second}
	resp, err := cl.Get("https://checkip.amazonaws.com")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 100))
	if ip := net.ParseIP(strings.TrimSpace(string(b))); ip != nil && ip.To4() != nil {
		return ip.String()
	}
	return ""
}

// normalizeCIDR accepts "1.2.3.4" or "1.2.3.0/24" and refuses anything broader than /16.
func normalizeCIDR(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "/") {
		s += "/32"
	}
	ip, n, err := net.ParseCIDR(s)
	if err != nil || ip.To4() == nil {
		return "", fmt.Errorf("not a valid IPv4 address or CIDR: %s", s)
	}
	if ones, _ := n.Mask.Size(); ones < 16 {
		return "", fmt.Errorf("%s is too broad (smallest allowed is /16)", s)
	}
	return n.String(), nil
}

func ruleCovers(rules []Rule, ip string, port int) bool {
	p := net.ParseIP(ip)
	for _, r := range rules {
		if _, n, err := net.ParseCIDR(r.CIDR); err == nil && r.Port == port && p != nil && n.Contains(p) {
			return true
		}
	}
	return false
}
