package files

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const defaultPresignTTL = 15 * time.Minute

type Storage interface {
	PresignUpload(ctx context.Context, objectKey, contentType, userID string, size int64) (*UploadIntent, error)
	PresignDownload(ctx context.Context, objectKey string) (string, error)
	Stat(ctx context.Context, objectKey string) (*ObjectInfo, error)
	MarkComplete(ctx context.Context, objectKey string) error
	Delete(ctx context.Context, objectKey string) error
	PublicURL(objectKey string) string
	KeyFromURL(fileURL string) (string, error)
}

type UploadIntent struct {
	UploadURL string            `json:"upload_url"`
	ObjectKey string            `json:"object_key"`
	Headers   map[string]string `json:"headers"`
}

type ObjectInfo struct {
	Size        int64
	ContentType string
	UserID      string
}

type S3Config struct {
	Region        string
	Bucket        string
	PublicBaseURL string
	PresignTTL    time.Duration
}

type S3Store struct {
	client        *s3.Client
	presignClient *s3.PresignClient
	bucket        string
	publicBaseURL string
	presignTTL    time.Duration
}

func NewS3Store(ctx context.Context, cfg S3Config) (*S3Store, error) {
	cfg.Region = strings.TrimSpace(cfg.Region)
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	if cfg.Region == "" {
		return nil, fmt.Errorf("AWS_REGION is required")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("AWS_S3_BUCKET is required")
	}
	if cfg.PresignTTL <= 0 {
		cfg.PresignTTL = defaultPresignTTL
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS configuration: %w", err)
	}
	client := s3.NewFromConfig(awsCfg)

	publicBaseURL := strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/")
	if publicBaseURL == "" {
		publicBaseURL = fmt.Sprintf("https://%s.s3.%s.amazonaws.com", cfg.Bucket, cfg.Region)
	}
	parsedBaseURL, err := url.ParseRequestURI(publicBaseURL)
	if err != nil || parsedBaseURL.Scheme != "https" || parsedBaseURL.Host == "" {
		if err == nil {
			err = fmt.Errorf("URL must be an absolute HTTPS URL")
		}
		return nil, fmt.Errorf("invalid AWS_S3_PUBLIC_BASE_URL: %w", err)
	}

	return &S3Store{
		client:        client,
		presignClient: s3.NewPresignClient(client),
		bucket:        cfg.Bucket,
		publicBaseURL: publicBaseURL,
		presignTTL:    cfg.PresignTTL,
	}, nil
}

func (s *S3Store) PresignUpload(
	ctx context.Context,
	objectKey, contentType, userID string,
	size int64,
) (*UploadIntent, error) {
	result, err := s.presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(objectKey),
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
		Metadata: map[string]string{
			"user-id": userID,
		},
		Tagging: aws.String("status=pending"),
	}, s3.WithPresignExpires(s.presignTTL))
	if err != nil {
		return nil, fmt.Errorf("presigning S3 upload: %w", err)
	}

	return &UploadIntent{
		UploadURL: result.URL,
		ObjectKey: objectKey,
		Headers:   flattenHeaders(result.SignedHeader),
	}, nil
}

func (s *S3Store) PresignDownload(ctx context.Context, objectKey string) (string, error) {
	result, err := s.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	}, s3.WithPresignExpires(s.presignTTL))
	if err != nil {
		return "", fmt.Errorf("presigning S3 download: %w", err)
	}
	return result.URL, nil
}

func (s *S3Store) Stat(ctx context.Context, objectKey string) (*ObjectInfo, error) {
	result, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return nil, fmt.Errorf("reading S3 object metadata: %w", err)
	}

	return &ObjectInfo{
		Size:        aws.ToInt64(result.ContentLength),
		ContentType: aws.ToString(result.ContentType),
		UserID:      result.Metadata["user-id"],
	}, nil
}

func (s *S3Store) MarkComplete(ctx context.Context, objectKey string) error {
	_, err := s.client.PutObjectTagging(ctx, &s3.PutObjectTaggingInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
		Tagging: &types.Tagging{
			TagSet: []types.Tag{
				{Key: aws.String("status"), Value: aws.String("complete")},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("marking S3 upload complete: %w", err)
	}
	return nil
}

func (s *S3Store) Delete(ctx context.Context, objectKey string) error {
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	}); err != nil {
		return fmt.Errorf("deleting S3 object: %w", err)
	}
	return nil
}

func (s *S3Store) PublicURL(objectKey string) string {
	parts := strings.Split(objectKey, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return s.publicBaseURL + "/" + strings.Join(parts, "/")
}

func (s *S3Store) KeyFromURL(fileURL string) (string, error) {
	prefix := s.publicBaseURL + "/"
	if !strings.HasPrefix(fileURL, prefix) {
		return "", fmt.Errorf("file URL does not match configured S3 public URL")
	}
	key, err := url.PathUnescape(strings.TrimPrefix(fileURL, prefix))
	if err != nil {
		return "", fmt.Errorf("decoding S3 object key: %w", err)
	}
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "../") {
		return "", fmt.Errorf("invalid S3 object key")
	}
	return key, nil
}

func flattenHeaders(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		if strings.EqualFold(key, "host") || strings.EqualFold(key, "content-length") {
			continue
		}
		result[key] = strings.Join(values, ",")
	}
	return result
}
