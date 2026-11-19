package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// terminate saves diagnostics, releases the model volume cleanly and terminates the server in every region.
func (j *job) terminate() (string, error) {
	c := j.c
	if out, err := j.diag(); err != nil {
		j.logf("", "diagnostics skipped: %v", err)
	} else {
		j.logf("", "%s", out)
	}
	found, errs := eachRegion(c.Regions, func(r string) ([]string, error) {
		ins, err := projectInstances(c, r, "pending", "running", "stopping", "stopped")
		if err != nil || len(ins) == 0 {
			return nil, err
		}
		var ids []string
		for _, x := range ins {
			in := toInstance(x, r)
			ids = append(ids, in.ID)
			vols, _ := describeVolumesIn(c, r, filt("attachment.instance-id", in.ID), filt("tag:Name", c.ModelVolume.Name))
			if len(vols) == 0 || in.IP == "" {
				continue
			}
			j.logf(r, "unmounting the model volume cleanly on %s (%s)", in.ID, in.IP)
			if out, err := sh(60*time.Second, "ssh", append(sshOpts(c), c.SSHUser+"@"+in.IP,
				"sudo docker stop dt >/dev/null 2>&1; sync; sudo umount /opt/models")...); err != nil {
				j.logf(r, "could not unmount cleanly: %s", firstLine(out))
			}
			for _, v := range vols {
				ctx, cancel := ctxT(30 * time.Second)
				if _, err := ec2c(r).DetachVolume(ctx, &ec2.DetachVolumeInput{VolumeId: aws.String(v.ID)}); err != nil {
					j.logf(r, "detach %s failed: %s", v.ID, apiErr(err))
				} else {
					j.logf(r, "detached %s", v.ID)
				}
				cancel()
			}
		}
		ctx, cancel := ctxT(30 * time.Second)
		defer cancel()
		if _, err := ec2c(r).TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: ids}); err != nil {
			return nil, fmt.Errorf("%s: terminate: %s", r, apiErr(err))
		}
		j.logf(r, "terminating %s", strings.Join(ids, " "))
		return ids, nil
	})
	var all []string
	for i, ids := range found {
		if errs[i] != nil {
			return "", errs[i]
		}
		all = append(all, ids...)
	}
	if len(all) == 0 {
		return "nothing was running", nil
	}
	return "terminating " + strings.Join(all, " "), nil
}

// freeVolumes force-detaches the model volume from any server that is still shutting down,
// so a new server can use it at once. The old server finishes terminating on its own.
func (j *job) freeVolumes() (string, error) {
	c := j.c
	n := 0
	var mu sync.Mutex
	_, errs := eachRegion(c.Regions, func(r string) (struct{}, error) {
		ins, err := projectInstances(c, r, "shutting-down")
		if err != nil {
			return struct{}{}, err
		}
		for _, x := range ins {
			id := aws.ToString(x.InstanceId)
			vols, _ := describeVolumesIn(c, r, filt("attachment.instance-id", id), filt("tag:Name", c.ModelVolume.Name))
			for _, v := range vols {
				j.logf(r, "force-detaching %s from %s", v.ID, id)
				ctx, cancel := ctxT(5 * time.Minute)
				if _, err := ec2c(r).DetachVolume(ctx, &ec2.DetachVolumeInput{VolumeId: aws.String(v.ID), Force: aws.Bool(true)}); err != nil {
					j.logf(r, "detach %s: %s", v.ID, apiErr(err))
				}
				if err := ec2.NewVolumeAvailableWaiter(ec2c(r)).Wait(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{v.ID}}, 5*time.Minute); err != nil {
					j.logf(r, "%s still not free: %v", v.ID, err)
				} else {
					j.logf(r, "%s is free", v.ID)
					mu.Lock()
					n++
					mu.Unlock()
				}
				cancel()
			}
		}
		return struct{}{}, nil
	})
	for _, e := range errs {
		if e != nil {
			return "", e
		}
	}
	if n == 0 {
		return "no volume needed freeing", nil
	}
	return fmt.Sprintf("freed %d volume(s)", n), nil
}

// deleteVolumes deletes this project's volumes that no server uses, in every region.
func (j *job) deleteVolumes() (string, error) {
	c := j.c
	var n atomic.Int32
	_, errs := eachRegion(c.Regions, func(r string) (struct{}, error) {
		vols, err := describeVolumesIn(c, r)
		if err != nil {
			return struct{}{}, err
		}
		for _, v := range vols {
			if v.State != "available" {
				j.logf(r, "left alone (%s): %s", v.State, v.ID)
				continue
			}
			ctx, cancel := ctxT(30 * time.Second)
			if _, err := ec2c(r).DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(v.ID)}); err != nil {
				j.logf(r, "delete %s failed: %s", v.ID, apiErr(err))
			} else {
				j.logf(r, "deleted %s (%s, %dGB)", v.ID, v.AZ, v.SizeGB)
				n.Add(1)
			}
			cancel()
		}
		return struct{}{}, nil
	})
	for _, e := range errs {
		if e != nil {
			return "", e
		}
	}
	return fmt.Sprintf("deleted %d unused volume(s)", n.Load()), nil
}

// diag copies the server's logs to logs/diag-<time>/.
func (j *job) diag() (string, error) {
	in, err := describeInstance(j.c)
	if err != nil {
		return "", err
	}
	if in == nil || in.State != "running" || in.IP == "" {
		return "", fmt.Errorf("no running server")
	}
	dir := filepath.Join(j.root, "logs", "diag-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	files := [][2]string{
		{"timeline.csv", "cat /var/log/dt-timeline.log"},
		{"server-events.log", "cat /var/log/dt-server-events.log"},
		{"events.log", "cat /var/log/dt-events.log"},
		{"boot.log", "cat /var/log/drawthings-boot.log /var/log/dt-image.log"},
		{"container.log", `sudo docker logs dt 2>&1 | grep -v -E "mDNS|CUFILE" | tail -500`},
		{"system.txt", "nvidia-smi -q | head -80; echo; lscpu | head -20; echo; free -m; echo; df -h /opt/models /"},
	}
	var wg sync.WaitGroup
	for _, f := range files {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := sh(60*time.Second, "ssh", append(sshOpts(j.c), j.c.SSHUser+"@"+in.IP, f[1])...)
			if err != nil {
				j.logf("", "diag: could not fetch %s: %s", f[0], truncate(firstLine(out), 200))
				return
			}
			_ = os.WriteFile(filepath.Join(dir, f[0]), []byte(out+"\n"), 0o644)
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(filepath.Join(dir, "timeline.csv"))
	rows := strings.Count(string(b), "\n")
	if rows <= 1 {
		j.logf("", "diag: no timeline rows, the server may predate the timeline logger")
	}
	rel, _ := filepath.Rel(j.root, dir)
	return fmt.Sprintf("diagnostics saved to %s (%d timeline rows)", rel, rows), nil
}

// countingReader tracks bytes read so an upload can report progress.
type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// upload copies required models that are missing from S3, or differ in size, from this Mac.
func (j *job) upload(s3sizes map[string]int64) (string, error) {
	c := j.c
	var todo []string
	for _, f := range c.wanted() {
		st, err := os.Stat(filepath.Join(c.LocalModels, f))
		if err != nil {
			j.logf("", "skipping %s: not on this Mac", f)
			continue
		}
		if s, ok := s3sizes[f]; !ok || s != st.Size() {
			todo = append(todo, f)
		}
	}
	if len(todo) == 0 {
		return "nothing to upload, S3 already matches", nil
	}
	tm := transfermanager.New(s3c(c.BucketRegion), func(o *transfermanager.Options) {
		o.PartSizeBytes, o.Concurrency = 64<<20, 8
	})
	for _, f := range todo {
		fh, err := os.Open(filepath.Join(c.LocalModels, f))
		if err != nil {
			return "", err
		}
		st, _ := fh.Stat()
		var n atomic.Int64
		t := time.Now()
		j.logf("", "uploading %s (%s)", f, gib(st.Size()))
		done := make(chan struct{})
		go func() {
			for {
				select {
				case <-done:
					return
				case <-time.After(30 * time.Second):
					got := n.Load()
					j.logf("", "  %s: %s of %s, %.0f MiB/s", f, gib(got), gib(st.Size()), float64(got)/(1<<20)/time.Since(t).Seconds())
				}
			}
		}()
		_, err = tm.UploadObject(context.Background(), &transfermanager.UploadObjectInput{Bucket: aws.String(c.Bucket),
			Key: aws.String("models/" + f), Body: countingReader{fh, &n}})
		close(done)
		fh.Close()
		if err != nil {
			return "", fmt.Errorf("upload %s: %s", f, apiErr(err))
		}
		j.logf("", "uploaded %s in %s (%.0f MiB/s)", f, secs(t), float64(st.Size())/(1<<20)/time.Since(t).Seconds())
	}
	return fmt.Sprintf("uploaded %d file(s)", len(todo)), nil
}
