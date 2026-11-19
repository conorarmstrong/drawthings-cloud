package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// Profile overrides a subset of settings. Unset fields leave the config alone.
type Profile struct {
	InstanceType string `json:"instance_type,omitempty"`
	Spot         *bool  `json:"spot,omitempty"`
	IdleMin      int    `json:"idle_minutes,omitempty"`
	MaxHours     int    `json:"max_hours,omitempty"`
	CPUOffload   *bool  `json:"cpu_offload,omitempty"`
}

type ModelVolume struct {
	Enabled        bool   `json:"enabled"`
	Name           string `json:"name"`
	SizeGB         int    `json:"size_gb"`
	IOPS           int    `json:"iops"`
	ThroughputMiBs int    `json:"throughput_mibs"`
	DeleteAfterMin int    `json:"delete_after_minutes"`
}

// Config is the single source of truth, read from config.json.
type Config struct {
	Region       string   `json:"region"`
	Regions      []string `json:"regions"` // launch order, preferred first; Region mirrors Regions[0]
	Active       string   `json:"-"`       // region of the running server, when there is one
	Browse       string   `json:"-"`       // region last launched in, used for browsing while no server runs
	Bucket       string   `json:"bucket"`
	BucketRegion string   `json:"bucket_region"`
	InstanceType string   `json:"instance_type"`
	Spot         bool     `json:"spot"`
	IdleMin      int      `json:"idle_minutes"`
	MaxHours     int      `json:"max_hours"`
	VolumeGB     int      `json:"volume_gb"`
	CPUOffload   bool     `json:"cpu_offload"`
	AllowedCIDRs []string `json:"allowed_cidrs"`
	SharedSecret string   `json:"shared_secret"`

	ModelVolume ModelVolume `json:"model_volume"`

	LocalPort   int                 `json:"local_port"`
	LocalModels string              `json:"local_models_dir"`
	Models      []string            `json:"models"`               // always synced
	ModelSets   map[string][]string `json:"model_sets,omitempty"` // named extras, one is synced with Models
	ModelSet    string              `json:"model_set,omitempty"`

	KeyName     string `json:"key_name"`
	KeyFile     string `json:"ssh_key_file"`
	SSHUser     string `json:"ssh_user"`
	RoleName    string `json:"role_name"`
	SGName      string `json:"sg_name"`
	ProjectTag  string `json:"project_tag"`
	DockerImage string `json:"docker_image"`
	ImageCache  bool   `json:"image_cache"`
	AMIParam    string `json:"ami_ssm_parameter"`
	ServerPort  int    `json:"server_port"`

	GPUIdlePct     int `json:"gpu_idle_percent"`
	BootTimeoutMin int `json:"boot_timeout_minutes"`

	CandidateTypes   []string          `json:"candidate_instance_types"`
	FallbackTypes    []string          `json:"fallback_instance_types"` // tried, in order, when the main type has no capacity anywhere
	RetryMin         int               `json:"launch_retry_minutes"`    // keep retrying a failed launch for this long
	PricingLocations map[string]string `json:"pricing_locations"`

	WarnAfterHours   int     `json:"warn_after_hours"`
	MonthlyBudgetUSD float64 `json:"monthly_budget_usd"`
	Notifications    bool    `json:"notifications"`

	Profile  string             `json:"profile"`
	Profiles map[string]Profile `json:"profiles"`
}

func bptr(b bool) *bool { return &b }

func defaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Region:         "eu-west-2",
		Bucket:         "",
		BucketRegion:   "",
		InstanceType:   "g7e.2xlarge",
		Spot:           true,
		RetryMin:       10,
		FallbackTypes:  []string{"g7e.4xlarge"},
		IdleMin:        10,
		MaxHours:       6,
		VolumeGB:       200,
		AllowedCIDRs:   []string{"auto"},
		ModelVolume:    ModelVolume{Enabled: true, Name: "drawthings-models", SizeGB: 100, IOPS: 3000, ThroughputMiBs: 500, DeleteAfterMin: 30},
		LocalPort:      7859,
		LocalModels:    filepath.Join(home, "Library/Containers/com.liuliu.draw-things/Data/Documents/Models"),
		KeyName:        "drawthings",
		KeyFile:        "~/.ssh/drawthings_aws",
		SSHUser:        "ubuntu",
		RoleName:       "drawthings-server",
		SGName:         "drawthings-ssh",
		ProjectTag:     "drawthings",
		DockerImage:    "drawthingsai/draw-things-grpc-server-cli:latest",
		ImageCache:     true,
		AMIParam:       "/aws/service/deeplearning/ami/x86_64/base-oss-nvidia-driver-gpu-ubuntu-22.04/latest/ami-id",
		ServerPort:     7859,
		GPUIdlePct:     5,
		BootTimeoutMin: 60,
		WarnAfterHours: 3,
		Notifications:  true,
	}
}

// reg is the region to query: where the server runs, else the preferred one.
func (c Config) reg() string {
	if c.Active != "" {
		return c.Active
	}
	if c.Browse != "" {
		return c.Browse
	}
	return c.Region
}

func fillDefaults(c *Config) {
	if len(c.Regions) == 0 {
		c.Regions = []string{"eu-west-1", "eu-west-2", "eu-central-1", "eu-north-1", "us-east-1", "us-east-2", "us-west-2"}
	}
	c.Region = c.Regions[0]
	if len(c.CandidateTypes) == 0 {
		c.CandidateTypes = []string{"g7e.2xlarge", "g7e.4xlarge", "p5.4xlarge", "g6e.2xlarge", "g6e.4xlarge", "g5.2xlarge"}
	}
	if len(c.PricingLocations) == 0 {
		c.PricingLocations = map[string]string{
			"eu-west-1": "EU (Ireland)", "eu-west-2": "EU (London)", "eu-west-3": "EU (Paris)",
			"eu-central-1": "EU (Frankfurt)", "eu-north-1": "EU (Stockholm)",
			"us-east-1": "US East (N. Virginia)", "us-east-2": "US East (Ohio)", "us-west-2": "US West (Oregon)",
		}
	}
	if len(c.Profiles) == 0 {
		c.Profiles = map[string]Profile{
			"cheap": {Spot: bptr(true), IdleMin: 10},
			"safe":  {Spot: bptr(false), IdleMin: 30, MaxHours: 8},
		}
	}
}

func loadConfig(path string) Config {
	c := defaultConfig()
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	fillDefaults(&c)
	c.LocalModels = expandHome(c.LocalModels)
	return c
}

func (c Config) save(path string) {
	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(path, append(b, '\n'), 0o644)
}

// wanted is the file list for this launch: Models plus the active model set.
func (c Config) wanted() []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range append(append([]string{}, c.Models...), c.ModelSets[c.ModelSet]...) {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

func (c Config) modelSetNames() []string {
	var n []string
	for k := range c.ModelSets {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func (c Config) profileNames() []string {
	var n []string
	for k := range c.Profiles {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func (c *Config) applyProfile(name string) {
	p, ok := c.Profiles[name]
	if !ok {
		return
	}
	c.Profile = name
	if p.InstanceType != "" {
		c.InstanceType = p.InstanceType
	}
	if p.Spot != nil {
		c.Spot = *p.Spot
	}
	if p.IdleMin > 0 {
		c.IdleMin = p.IdleMin
	}
	if p.MaxHours > 0 {
		c.MaxHours = p.MaxHours
	}
	if p.CPUOffload != nil {
		c.CPUOffload = *p.CPUOffload
	}
}
