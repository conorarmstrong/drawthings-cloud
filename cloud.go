package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	et "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

// AWS clients, one per service and region, built once from the usual ~/.aws credentials and profile.
var (
	baseOnce sync.Once
	baseCfg  aws.Config
	baseErr  error
	clMu     sync.Mutex
	ec2s     = map[string]*ec2.Client{}
	s3s      = map[string]*s3.Client{}
	ssms     = map[string]*ssm.Client{}
	lambdas  = map[string]*lambda.Client{}
	bridges  = map[string]*eventbridge.Client{}
	globals  = map[string]any{}
)

func awsBase() aws.Config {
	baseOnce.Do(func() { baseCfg, baseErr = config.LoadDefaultConfig(context.Background()) })
	return baseCfg
}

func regional[T any](m map[string]T, region string, mk func(aws.Config) T) T {
	clMu.Lock()
	defer clMu.Unlock()
	if c, ok := m[region]; ok {
		return c
	}
	cfg := awsBase().Copy()
	cfg.Region = region
	c := mk(cfg)
	m[region] = c
	return c
}

func ec2c(r string) *ec2.Client {
	return regional(ec2s, r, func(c aws.Config) *ec2.Client { return ec2.NewFromConfig(c) })
}
func s3c(r string) *s3.Client {
	return regional(s3s, r, func(c aws.Config) *s3.Client { return s3.NewFromConfig(c) })
}
func ssmc(r string) *ssm.Client {
	return regional(ssms, r, func(c aws.Config) *ssm.Client { return ssm.NewFromConfig(c) })
}
func lambdac(r string) *lambda.Client {
	return regional(lambdas, r, func(c aws.Config) *lambda.Client { return lambda.NewFromConfig(c) })
}
func ebc(r string) *eventbridge.Client {
	return regional(bridges, r, func(c aws.Config) *eventbridge.Client { return eventbridge.NewFromConfig(c) })
}

// IAM and STS are global; the Pricing API only answers in us-east-1.
func iamc() *iam.Client {
	return regional(globals, "iam", func(c aws.Config) any { c.Region = "us-east-1"; return iam.NewFromConfig(c) }).(*iam.Client)
}
func stsc() *sts.Client {
	return regional(globals, "sts", func(c aws.Config) any { c.Region = "us-east-1"; return sts.NewFromConfig(c) }).(*sts.Client)
}
func pricingc() *pricing.Client {
	return regional(globals, "pricing", func(c aws.Config) any { c.Region = "us-east-1"; return pricing.NewFromConfig(c) }).(*pricing.Client)
}

func ctxT(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// apiErr is a short readable form of an AWS error: "Code: message".
func apiErr(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode() + ": " + ae.ErrorMessage()
	}
	if baseErr != nil {
		return "AWS config: " + baseErr.Error()
	}
	return err.Error()
}

func errCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

func filt(name string, values ...string) et.Filter {
	return et.Filter{Name: aws.String(name), Values: values}
}

func tags(kv ...string) []et.Tag {
	var t []et.Tag
	for i := 0; i+1 < len(kv); i += 2 {
		t = append(t, et.Tag{Key: aws.String(kv[i]), Value: aws.String(kv[i+1])})
	}
	return t
}
