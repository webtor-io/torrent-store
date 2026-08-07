package providers

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
	cs "github.com/webtor-io/common-services"
	ss "github.com/webtor-io/torrent-store/services"
	"io"
)

const (
	AWSBucketFlag = "aws-bucket"
	S3UseFlag     = "use-s3"
)

func RegisterS3Flags(f []cli.Flag) []cli.Flag {
	return append(f,
		cli.StringFlag{
			Name:   AWSBucketFlag,
			Usage:  "s3 store bucket",
			Value:  "torrent-store",
			EnvVar: "AWS_BUCKET",
		},
		cli.BoolFlag{
			Name:   S3UseFlag,
			Usage:  "use s3",
			EnvVar: "USE_S3",
		},
	)
}

type S3 struct {
	bucket string
	cl     *cs.S3Client
}

func NewS3(c *cli.Context, cl *cs.S3Client) *S3 {
	if !c.Bool(S3UseFlag) {
		return nil
	}
	return &S3{
		bucket: c.String(AWSBucketFlag),
		cl:     cl,
	}
}

func (s *S3) Name() string {
	return "s3"
}

func (s *S3) Touch(ctx context.Context, h string) (ok bool, err error) {
	cl := s.cl.Get()
	r, err := cl.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(h),
	})
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok && awsErr.Code() == s3.ErrCodeNoSuchKey {
			return false, ss.ErrNotFound
		}
		return false, err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(r.Body)
	return true, nil
}

func (s *S3) makeAWSMD5(b []byte) *string {
	h := md5.Sum(b)
	m := base64.StdEncoding.EncodeToString(h[:])
	return aws.String(m)
}

func (s *S3) Push(ctx context.Context, h string, torrent []byte) (ok bool, err error) {
	cl := s.cl.Get()
	_, err = cl.PutObjectWithContext(ctx,
		&s3.PutObjectInput{
			Bucket:     aws.String(s.bucket),
			Key:        aws.String(h),
			Body:       bytes.NewReader(torrent),
			ContentMD5: s.makeAWSMD5(torrent),
		})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *S3) Pull(ctx context.Context, h string) (torrent []byte, err error) {
	cl := s.cl.Get()
	r, err := cl.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(h),
	})
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok && awsErr.Code() == s3.ErrCodeNoSuchKey {
			return nil, ss.ErrNotFound
		}
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(r.Body)
	return io.ReadAll(r.Body)
}

// s3DerivedKey namespaces derived blobs with a per-kind suffix so they sit
// next to the raw .torrent object (stored under the bare infoHash) without
// colliding. Derived blobs are immutable and rebuildable, so they live
// without an expiry — the durable bottom tier that survives Badger/Redis
// eviction. The suffixes are FROZEN: objects written under them are still
// being read.
func s3DerivedKey(kind ss.DerivedKind, h string) (string, error) {
	switch kind {
	case ss.DerivedManifest:
		return h + ".manifest", nil
	case ss.DerivedFingerprint:
		return h + ".fingerprint", nil
	}
	return "", errors.Errorf("no s3 key mapping for derived kind %q", kind)
}

func (s *S3) PushDerived(ctx context.Context, kind ss.DerivedKind, h string, blob []byte) (ok bool, err error) {
	key, err := s3DerivedKey(kind, h)
	if err != nil {
		return false, err
	}
	_, err = s.cl.Get().PutObjectWithContext(ctx,
		&s3.PutObjectInput{
			Bucket:     aws.String(s.bucket),
			Key:        aws.String(key),
			Body:       bytes.NewReader(blob),
			ContentMD5: s.makeAWSMD5(blob),
		})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *S3) PullDerived(ctx context.Context, kind ss.DerivedKind, h string) (blob []byte, err error) {
	key, err := s3DerivedKey(kind, h)
	if err != nil {
		return nil, err
	}
	r, err := s.cl.Get().GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok && awsErr.Code() == s3.ErrCodeNoSuchKey {
			return nil, ss.ErrNotFound
		}
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(r.Body)
	return io.ReadAll(r.Body)
}

var _ ss.StoreProvider = (*S3)(nil)
