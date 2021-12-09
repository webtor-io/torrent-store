package providers

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"io"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/urfave/cli"
	cs "github.com/webtor-io/common-services"
	ss "github.com/webtor-io/torrent-store/services"
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

func (s *S3) Touch(h string) (err error) {
	cl := s.cl.Get()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	r, err := cl.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(h),
	})
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok && awsErr.Code() == s3.ErrCodeNoSuchKey {
			return ss.ErrNotFound
		}
		return err
	}
	defer r.Body.Close()
	return nil
}

func (s *S3) makeAWSMD5(b []byte) *string {
	h := md5.Sum(b)
	m := base64.StdEncoding.EncodeToString(h[:])
	return aws.String(m)
}

func (s *S3) Push(h string, torrent []byte) (err error) {
	cl := s.cl.Get()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err = cl.PutObjectWithContext(ctx,
		&s3.PutObjectInput{
			Bucket:     aws.String(s.bucket),
			Key:        aws.String(h),
			Body:       bytes.NewReader(torrent),
			ContentMD5: s.makeAWSMD5(torrent),
		})
	return
}

func (s *S3) Pull(h string) (torrent []byte, err error) {
	cl := s.cl.Get()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
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
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}
