package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type Config struct {
	Endpoint, PublicEndpoint, Region, Bucket, AccessKey, SecretKey, CORSOrigin string
	ServerSideEncryption, KMSKeyID                                             string
	UsePathStyle                                                               bool
}

type CompletedPart struct {
	PartNumber int32  `json:"part_number"`
	ETag       string `json:"etag"`
}

func (s *S3) CreateMultipart(ctx context.Context, key, contentType string) (string, error) {
	output, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(key), ContentType: aws.String(contentType), ServerSideEncryption: s.encryption, SSEKMSKeyId: s.kmsKeyID})
	if err != nil {
		return "", fmt.Errorf("create multipart upload: %w", err)
	}
	return aws.ToString(output.UploadId), nil
}
func (s *S3) PresignUploadPart(ctx context.Context, key, uploadID string, partNumber int32, lifetime time.Duration) (string, error) {
	output, err := s.presigner.PresignUploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID), PartNumber: aws.Int32(partNumber)}, s3.WithPresignExpires(lifetime))
	if err != nil {
		return "", fmt.Errorf("presign upload part: %w", err)
	}
	return output.URL, nil
}
func (s *S3) CompleteMultipart(ctx context.Context, key, uploadID string, parts []CompletedPart) error {
	completed := make([]types.CompletedPart, len(parts))
	for i, part := range parts {
		completed[i] = types.CompletedPart{PartNumber: aws.Int32(part.PartNumber), ETag: aws.String(part.ETag)}
	}
	_, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID), MultipartUpload: &types.CompletedMultipartUpload{Parts: completed}})
	if err != nil {
		return fmt.Errorf("complete multipart upload: %w", err)
	}
	return nil
}
func (s *S3) ListParts(ctx context.Context, key, uploadID string) ([]CompletedPart, error) {
	output, err := s.client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID)})
	if err != nil {
		return nil, fmt.Errorf("list multipart parts: %w", err)
	}
	parts := make([]CompletedPart, 0, len(output.Parts))
	for _, part := range output.Parts {
		parts = append(parts, CompletedPart{PartNumber: aws.ToInt32(part.PartNumber), ETag: strings.Trim(aws.ToString(part.ETag), `"`)})
	}
	return parts, nil
}
func (s *S3) AbortMultipart(ctx context.Context, key, uploadID string) error {
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID)})
	var apiErr interface{ ErrorCode() string }
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchUpload" {
		return nil
	}
	return err
}
func (s *S3) ObjectSize(ctx context.Context, key string) (int64, error) {
	output, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return 0, fmt.Errorf("head object: %w", err)
	}
	return aws.ToInt64(output.ContentLength), nil
}

type S3 struct {
	client     *s3.Client
	presigner  *s3.PresignClient
	bucket     string
	encryption types.ServerSideEncryption
	kmsKeyID   *string
}

func NewS3(ctx context.Context, cfg Config) (*S3, error) {
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	// Cloudlet streams uploads while calculating its own SHA-256 checksum.
	// Avoid the SDK's optional trailing checksum, which requires a seekable body over local HTTP.
	awsCfg.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	newClient := func(endpoint string) *s3.Client {
		return s3.NewFromConfig(awsCfg, func(options *s3.Options) {
			options.UsePathStyle = cfg.UsePathStyle
			if endpoint != "" {
				options.BaseEndpoint = aws.String(endpoint)
			}
		})
	}
	client := newClient(cfg.Endpoint)
	presignClient := client
	if cfg.PublicEndpoint != "" && cfg.PublicEndpoint != cfg.Endpoint {
		presignClient = newClient(cfg.PublicEndpoint)
	}
	store := &S3{client: client, presigner: s3.NewPresignClient(presignClient), bucket: cfg.Bucket, encryption: types.ServerSideEncryption(cfg.ServerSideEncryption)}
	if cfg.KMSKeyID != "" {
		store.kmsKeyID = aws.String(cfg.KMSKeyID)
	}
	if err := store.ensureBucket(ctx); err != nil {
		return nil, err
	}
	if err := store.ensureCORS(ctx, cfg.CORSOrigin); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *S3) ensureCORS(ctx context.Context, origin string) error {
	if origin == "" {
		origin = "*"
	}
	_, err := s.client.PutBucketCors(ctx, &s3.PutBucketCorsInput{
		Bucket:            aws.String(s.bucket),
		CORSConfiguration: &types.CORSConfiguration{CORSRules: []types.CORSRule{{AllowedMethods: []string{"GET", "HEAD", "PUT"}, AllowedOrigins: []string{origin}, AllowedHeaders: []string{"*"}, ExposeHeaders: []string{"ETag"}}}},
	})
	if err != nil {
		var apiErr interface{ ErrorCode() string }
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NotImplemented" {
			// Some S3-compatible stores (including certain MinIO modes) do not
			// implement bucket CORS. Presigned URLs still work; do not block API startup.
			return nil
		}
		return fmt.Errorf("configure bucket CORS: %w", err)
	}
	return nil
}

func (s *S3) PresignGet(ctx context.Context, key string, lifetime time.Duration) (string, error) {
	request, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(lifetime))
	if err != nil {
		return "", fmt.Errorf("presign object: %w", err)
	}
	return request.URL, nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("get object: %w", err)
	}
	return result.Body, nil
}

func (s *S3) Put(ctx context.Context, key, contentType string, body io.Reader, size int64) error {
	temporary, err := os.CreateTemp("", "cloudlet-upload-*")
	if err != nil {
		return fmt.Errorf("create upload spool: %w", err)
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	written, err := io.Copy(temporary, body)
	if err != nil {
		return fmt.Errorf("spool upload: %w", err)
	}
	if written != size {
		return fmt.Errorf("upload size mismatch: expected %d, received %d", size, written)
	}
	if _, err = temporary.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind upload: %w", err)
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: temporary,
		ContentLength: aws.Int64(size), ContentType: aws.String(contentType), ServerSideEncryption: s.encryption, SSEKMSKeyId: s.kmsKeyID})
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}

func (s *S3) ensureBucket(ctx context.Context) error {
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err == nil {
		return nil
	}
	if _, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return fmt.Errorf("create bucket: %w", err)
	}
	return nil
}
